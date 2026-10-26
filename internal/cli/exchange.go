package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// observer sees the messages of an exchange as they pass.
type observer interface {
	// request is called before each request is sent; retry marks the
	// request repeated with Digest credentials.
	request(r *request, retry bool) error
	// sent is called once a request is sent and its response's head read.
	sent(r *request) error
	// response is called with each response shown: the last, and the
	// redirects that --all or --verbose ask for.
	response(r *http.Response, headers []header, start time.Time) error
}

type redirectLimitError int

func (e redirectLimitError) Error() string {
	return fmt.Sprintf("too many redirects (--max-redirects=%d)", int(e))
}

// exchange is a request built from arguments, with its session and
// authentication, and the transport that sends it.
type exchange struct {
	options   options
	request   *request
	session   *session
	auth      authentication
	transport *transport
	body      io.Closer
	cleanup   func()
	closed    bool
}

func prepare(o options, program string, stdin io.Reader, stdinTTY bool) (*exchange, error) {
	req, cleanup, err := buildRequest(o, program, stdin, stdinTTY)
	if err != nil {
		return nil, err
	}
	e := &exchange{options: o, request: req, body: req.Body, cleanup: cleanup, session: &session{cookies: []storedCookie{}}}
	if err = e.authenticate(); err != nil {
		e.close()
		return nil, err
	}
	return e, nil
}

func (e *exchange) authenticate() (err error) {
	o, req := e.options, e.request
	if name := o.value("session"); name != "" || o.has("session-read-only") {
		if name == "" {
			name = o.value("session-read-only")
		}
		if e.session, err = loadSession(req.Host, name); err != nil {
			return err
		}
		e.session.apply(req, o)
	}
	s := e.session
	if e.auth, err = authCredentials(o, req); err != nil {
		return err
	}
	if s.path != "" {
		if e.auth.kind == "" {
			e.auth = s.credentials()
		} else {
			s.auth = map[string]any{"type": e.auth.kind, "raw_auth": e.auth.raw}
		}
		if err = s.updateHeaders(req, o); err != nil {
			return err
		}
	}
	if e.auth.kind == "" && !o.has("ignore-netrc") {
		if user, password, ok := netrcAuth(req.URL.Hostname()); ok {
			e.auth = authentication{kind: "basic", username: user, password: password, latin1: true}
		}
	}
	if err = applyAuth(req, e.auth); err != nil {
		return err
	}
	if kind := e.auth.kind; kind != "" && kind != "basic" && kind != "digest" && kind != "bearer" {
		return fmt.Errorf("unsupported session authentication type: %s", kind)
	}
	if cookie := s.cookiesFor(req.URL); cookie != "" && req.Header.Get("Cookie") == "" {
		req.setHeader("Cookie", cookie)
		req.moveHeaderBefore("Cookie", "User-Agent")
	}
	return nil
}

// open sets up the transport, apart from prepare so that the CLI's update
// warnings keep their place between the two.
func (e *exchange) open() (err error) {
	e.transport, err = newTransport(e.options)
	return err
}

// close disconnects first, so that closing an unread response body after
// it does not drain the rest from the network.
func (e *exchange) close() {
	if e.closed {
		return
	}
	e.closed = true
	if e.transport != nil {
		e.transport.Close()
		e.transport.tls.close()
	}
	if e.body != nil {
		_ = e.body.Close()
	}
	e.cleanup()
}

func (e *exchange) saveSession() error {
	if e.session.path == "" || e.options.has("session-read-only") && e.session.exists {
		return nil
	}
	return e.session.save()
}

// do sends the request, follows redirects and answers a Digest challenge
// as the options ask, and returns the last response with its body unread.
func (e *exchange) do(ctx context.Context, obs observer) (*http.Response, error) {
	o, t, r, s, auth := e.options, e.transport, e.request, e.session, e.auth
	follow := o.has("follow") || o.has("download")
	history := o.has("all") || o.has("verbose")
	redirects := 0
	digestTried := false
	for {
		if err := obs.request(r, digestTried); err != nil {
			return nil, err
		}
		start := time.Now()
		response, headers, err := t.send(ctx, r)
		if err != nil {
			return nil, err
		}
		if err = obs.sent(r); err != nil {
			t.Close()
			response.Body.Close()
			return nil, err
		}
		s.receiveCookies(response.Cookies(), r.URL)
		redirect := response.Header.Get("Location") != "" && (response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308)
		challenge, offered := digestChallenge(response.Header.Values("WWW-Authenticate"))
		digest := auth.kind == "digest" && !digestTried && response.StatusCode == 401 && offered
		if redirect {
			redirects++
			if max := o.integer("max-redirects"); max != 0 && redirects == max {
				t.Close()
				response.Body.Close()
				return nil, redirectLimitError(max)
			}
		}
		redirect = follow && redirect
		if !digest && (history || !redirect) {
			if err = obs.response(response, headers, start); err != nil {
				t.Close()
				response.Body.Close()
				return nil, err
			}
		}
		if !redirect && !digest {
			return response, nil
		}
		_, err = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		if response.Close {
			t.Close()
		}
		if digest {
			value, e := digestAuthorization(challenge, r.Method, r.URL.RequestURI(), auth)
			if e != nil {
				return nil, e
			}
			r.setHeader("Authorization", value)
			digestTried = true
		} else {
			digestTried = false
			next, e := r.URL.Parse(response.Header.Get("Location"))
			if e != nil {
				return nil, e
			}
			if next.Scheme != "http" && next.Scheme != "https" {
				return nil, fmt.Errorf("unsupported redirect scheme: %s", next.Scheme)
			}
			stripAuth := address(r.URL) != address(next) || r.URL.Scheme != next.Scheme
			if r.URL.Hostname() == next.Hostname() && r.URL.Scheme == "http" && next.Scheme == "https" && (r.URL.Port() == "" || r.URL.Port() == "80") && (next.Port() == "" || next.Port() == "443") {
				stripAuth = false
			}
			if stripAuth {
				r.deleteHeader("Authorization")
				auth.kind = ""
			}
			if user, password, ok := netrcAuth(next.Hostname()); ok {
				auth = authentication{kind: "basic", username: user, password: password, latin1: true}
				if err := applyAuth(r, auth); err != nil {
					return nil, err
				}
			}
			status := response.StatusCode
			if status == 303 && r.Method != "HEAD" || status == 302 && r.Method != "HEAD" || status == 301 && r.Method == "POST" {
				r.Method = "GET"
			}
			if status != 307 && status != 308 {
				r.Body = nil
				r.streamedBody = false
				r.liveInput = false
				r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
				r.ContentLength = 0
				r.TransferEncoding = nil
				for _, name := range []string{"Content-Length", "Content-Type", "Transfer-Encoding"} {
					r.deleteHeader(name)
				}
			}
			r.URL = next
			if r.defaultHost {
				r.setHeader("Host", next.Host)
			}
		}
		if !digest {
			r.deleteHeader("Cookie")
		}
		if cookie := s.cookiesFor(r.URL); cookie != "" && r.Header.Get("Cookie") == "" {
			r.setHeader("Cookie", cookie)
			r.moveHeaderBefore("Cookie", "User-Agent")
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			r.Body, err = r.GetBody()
			if err != nil {
				return nil, err
			}
		}
	}
}
