package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func Run(program string, args []string) int {
	if len(args) == 1 && args[0] == "--internal-check-updates" {
		path, _ := updateSettings()
		defer os.Remove(path + ".lock")
		if fetchUpdates() != nil {
			return 1
		}
		return 0
	}
	code, err := run(program, args, os.Stdin, os.Stdout, os.Stderr, term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())), term.IsTerminal(int(os.Stderr.Fd())))
	if err != nil {
		if errors.Is(err, syscall.EPIPE) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "\n%s: error: %s\n", program, err)
	}
	return code
}
func run(program string, args []string, stdin io.Reader, stdout, stderr io.Writer, stdinTTY, stdoutTTY, stderrTTY bool) (code int, err error) {
	if program == "httpie" {
		return runManager(args, stdout, stderr)
	}
	configured, configErr := configArgs(args)
	if configErr != nil {
		fmt.Fprintf(stderr, "\n%s: warning: %s\n", program, configErr)
	} else {
		args = configured
	}
	o, err := parseOptions(args)
	if err != nil {
		return 1, err
	}
	if o.has("debug") || o.has("traceback") {
		defer func() {
			if err != nil {
				_, _ = stderr.Write(debug.Stack())
			}
		}()
	}
	if o.has("version") {
		_, err = fmt.Fprintln(stdout, version)
		return 0, err
	}
	if o.has("help") || o.has("manual") {
		text := help
		if o.has("manual") {
			text = manual
		}
		_, err = io.WriteString(stdout, text)
		return 0, err
	}
	if o.value("auth-type") == "?" {
		_, err = fmt.Fprintln(stdout, "basic: Basic HTTP auth\nbearer: Bearer HTTP Auth\ndigest: Digest HTTP auth")
		return 0, err
	}
	if o.value("style") == "?" {
		_, err = fmt.Fprintln(stdout, strings.Join(availableStyles(), ", "))
		return 0, err
	}
	if o.has("debug") {
		fmt.Fprintf(stderr, "HTTPie %s\nGo %s\nPlatform %s/%s\nConfig directory: %s\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH, configDir())
	}
	if o.has("debug") && len(o.args) == 0 {
		return 0, nil
	}
	defer checkUpdateWarnings(stderr, o.count("quiet"))
	if o.has("offline") {
		delete(o.values, "download")
		delete(o.values, "continue")
	}
	writer, tty := stdout, stdoutTTY
	var outputFile *os.File
	if o.has("download") {
		writer = stderr
		tty = stderrTTY
	} else if o.has("output") {
		outputFile, err = os.OpenFile(expandPath(o.value("output")), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
		if err != nil {
			return 1, err
		}
		defer outputFile.Close()
		writer = outputFile
		tty = false
	}
	if o.has("quiet") && !(o.has("output") && !o.has("download")) {
		writer = io.Discard
	}
	out, err := newOutput(writer, tty, o)
	if err != nil {
		return 1, err
	}
	req, cleanup, err := buildRequest(o, program, stdin, stdinTTY)
	if err != nil {
		return 1, err
	}
	defer cleanup()
	if req.Body != nil {
		defer req.Body.Close()
	}
	s := &session{cookies: []storedCookie{}}
	if name := o.value("session"); name != "" || o.has("session-read-only") {
		if name == "" {
			name = o.value("session-read-only")
		}
		s, err = loadSession(req.Host, name)
		if err != nil {
			return 1, err
		}
		s.apply(req, o)
	}
	kind, credentials, err := authCredentials(o, req)
	if err != nil {
		return 1, err
	}
	if s.path != "" {
		if kind == "" {
			kind, credentials = s.credentials()
		} else {
			s.auth = map[string]any{"type": kind, "raw_auth": credentials}
		}
		s.updateHeaders(req, o)
	}
	applyAuth(req, kind, credentials)
	if kind != "" && kind != "basic" && kind != "digest" && kind != "bearer" {
		return 1, fmt.Errorf("unsupported session authentication type: %s", kind)
	}
	if cookie := s.cookiesFor(req.URL); cookie != "" && req.Header.Get("Cookie") == "" {
		req.setHeader("Cookie", cookie)
		req.moveHeaderBefore("Cookie", "User-Agent")
	}
	transport, err := newTransport(o)
	if err != nil {
		return 1, err
	}
	defer transport.tls.close()
	defer transport.Close()
	if o.has("offline") {
		err = out.writeRequest(req, out.selection())
		if err == nil && s.path != "" && (!o.has("session-read-only") || !s.exists) {
			err = s.save()
		}
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
	var resume int64
	if o.has("download") {
		resume, err = prepareDownload(req, o)
		if err != nil {
			return 1, err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	code, err = exchange(ctx, transport, req, s, kind, credentials, out, stdout, stderr, stdoutTTY, resume)
	if ctx.Err() != nil {
		return 130, nil
	}
	if err == nil && s.path != "" && (!o.has("session-read-only") || !s.exists) {
		err = s.save()
		if err != nil {
			code = 1
		}
	}
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			code = 2
		}
	}
	return code, err
}
func exchange(ctx context.Context, t *transport, r *request, s *session, kind, credentials string, out *output, stdout, stderr io.Writer, stdoutTTY bool, resume int64) (int, error) {
	o := out.options
	follow := o.has("follow") || o.has("download")
	history := o.has("all") || o.has("verbose")
	redirects := 0
	digestTried := false
	initialURL := r.URL
	for {
		start := time.Now()
		response, headers, err := t.send(ctx, r)
		if err != nil {
			return 1, err
		}
		s.receiveCookies(response.Cookies(), r.URL)
		redirect := follow && response.Header.Get("Location") != "" && (response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308)
		digest := kind == "digest" && !digestTried && response.StatusCode == 401 && strings.HasPrefix(strings.ToLower(response.Header.Get("WWW-Authenticate")), "digest ")
		selection := out.selection()
		if history || (!redirect && !digest) {
			if err = out.writeRequest(r, selection); err == nil && !out.tty && strings.Contains(selection, "B") && strings.ContainsAny(selection, "hbm") {
				_, err = io.WriteString(out.writer, "\n\n")
			}
			if err == nil {
				responseSelection := selection
				if o.has("download") {
					responseSelection = strings.ReplaceAll(responseSelection, "b", "")
				}
				err = out.writeResponse(response, headers, responseSelection, start)
			}
			if err != nil {
				t.Close()
				response.Body.Close()
				return 1, err
			}
		}
		if !redirect && !digest {
			code := 0
			if o.has("check-status") || o.has("download") {
				switch {
				case response.StatusCode >= 500:
					code = 5
				case response.StatusCode >= 400:
					code = 4
				case response.StatusCode >= 300 && !follow:
					code = 3
				}
			}
			if code == 0 && o.has("download") {

				if e := download(response, initialURL, o, stdout, stderr, stdoutTTY, out.tty, resume); e != nil {
					t.Close()
					response.Body.Close()
					return 1, e
				}
			}
			t.Close()
			response.Body.Close()
			if code != 0 && !stdoutTTY && o.count("quiet") < 2 {
				fmt.Fprintf(stderr, "\nhttp: warning: HTTP %s\n", response.Status)
			}
			return code, nil
		}
		if history && !out.tty && strings.Contains(selection, "b") && strings.ContainsAny(selection, "HB") {
			if _, err = io.WriteString(out.writer, "\n\n"); err != nil {
				t.Close()
				response.Body.Close()
				return 1, err
			}
		}
		_, err = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil {
			return 1, err
		}
		if response.Close {
			t.Close()
		}
		if digest {
			value, e := digestAuthorization(response.Header.Get("WWW-Authenticate"), r.Method, r.URL.RequestURI(), credentials)
			if e != nil {
				return 1, e
			}
			r.setHeader("Authorization", value)
			digestTried = true
		} else {
			redirects++
			if max := o.integer("max-redirects"); max > 0 && redirects >= max {
				return 6, fmt.Errorf("too many redirects (--max-redirects=%d)", max)
			}
			next, e := r.URL.Parse(response.Header.Get("Location"))
			if e != nil {
				return 1, e
			}
			if next.Scheme != "http" && next.Scheme != "https" {
				return 1, fmt.Errorf("unsupported redirect scheme: %s", next.Scheme)
			}
			stripAuth := r.URL.Hostname() != next.Hostname() || r.URL.Port() != next.Port() || r.URL.Scheme != next.Scheme
			if r.URL.Hostname() == next.Hostname() && r.URL.Scheme == "http" && next.Scheme == "https" && (r.URL.Port() == "" || r.URL.Port() == "80") && (next.Port() == "" || next.Port() == "443") {
				stripAuth = false
			}
			if stripAuth {
				r.deleteHeader("Authorization")
				kind = ""
			}
			status := response.StatusCode
			if status == 303 && r.Method != "HEAD" || status == 302 && r.Method != "HEAD" || status == 301 && r.Method == "POST" {
				r.Method = "GET"
			}
			if status != 307 && status != 308 {
				r.Body = nil
				r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
				r.ContentLength = 0
				r.TransferEncoding = nil
				for _, name := range []string{"Content-Length", "Content-Type", "Transfer-Encoding"} {
					r.deleteHeader(name)
				}
			}
			r.URL = next
			r.setHeader("Host", next.Host)
		}
		r.deleteHeader("Cookie")
		if cookie := s.cookiesFor(r.URL); cookie != "" {
			r.setHeader("Cookie", cookie)
			r.moveHeaderBefore("Cookie", "User-Agent")
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			r.Body, err = r.GetBody()
			if err != nil {
				return 1, err
			}
		}
	}
}
func (r *request) deleteHeader(name string) {
	r.Header.Del(name)
	for i := 0; i < len(r.headers); {
		if strings.EqualFold(r.headers[i].Name, name) {
			r.headers = append(r.headers[:i], r.headers[i+1:]...)
		} else {
			i++
		}
	}
}
