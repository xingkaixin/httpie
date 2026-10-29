package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/netproxy"
	"github.com/egoist/mygo/ui/editor"

	"httpie/internal/core"
)

const maxBody = 10 << 20

// streamEvery is how often a body streaming shows what came of it.
const streamEvery = 200 * time.Millisecond

type hop struct {
	Status   string
	Code     int
	Location string
}

type response struct {
	Status    string
	Code      int
	Headers   []core.Header
	Body      string
	JSON      bool
	Size      int
	Truncated bool
	Duration  time.Duration
	Redirects []hop
	Sent      string
	Cookies   []core.Cookie `json:"-"`
	Err       string
	At        time.Time
	view      *editor.State
	sentView  *editor.State
	streaming bool
}

// sendFlags go with every request the app sends, but not into the command
// it copies: --all reports redirects, --pretty=format formats the body, and
// the rest keep the server's key order and indent as displayBody does.
var sendFlags = []string{"--all", "--pretty=format", "--unsorted", "--format-options=json.indent:2"}

var hasScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

// systemProxy returns the options sending a request for rawURL through the
// system's proxy, as browsers do: core reads proxies only from the
// environment, which has none for an app opened from the Dock. Both
// schemes get one, as a redirect followed may change it. It may block
// while the system runs a PAC file.
func systemProxy(rawURL string) []string {
	// The URL is completed as core does, or ":3000" would be looked up
	// as no host rather than localhost, which goes direct.
	rawURL = strings.TrimPrefix(rawURL, "://")
	if !hasScheme.MatchString(rawURL) {
		if strings.HasPrefix(rawURL, ":") {
			rawURL = "localhost" + rawURL
		}
		rawURL = "http://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	var opts []string
	for _, scheme := range []string{"http", "https"} {
		v := *u
		v.Scheme = scheme
		if proxy, err := netproxy.Proxy(&http.Request{URL: &v}); err == nil && proxy != nil {
			opts = append(opts, "--proxy="+scheme+":"+proxy.String())
		}
	}
	return opts
}

// keySeparators are what parseItem splits an item at, which a key escapes.
var keySeparators = strings.NewReplacer(":", `\:`, ";", `\;`, "=", `\=`, "@", `\@`)

// requestArgs returns the http command's arguments for r.
func requestArgs(r *request) []string {
	var opts, items []string
	switch r.AuthType {
	case authBasic:
		opts = append(opts, "--auth="+r.User+":"+r.Password)
	case authDigest:
		opts = append(opts, "--auth-type=digest", "--auth="+r.User+":"+r.Password)
	case authBearer:
		opts = append(opts, "--auth-type=bearer", "--auth="+r.Token)
	}
	if r.Follow {
		opts = append(opts, "--follow")
	}
	if r.Insecure {
		opts = append(opts, "--verify=no")
	}
	if r.Timeout > 0 {
		opts = append(opts, "--timeout="+strconv.FormatFloat(r.Timeout, 'f', -1, 64))
	}
	for _, p := range enabled(r.Params) {
		v := p.Value
		// "==@" would read the value from a file.
		if strings.HasPrefix(v, "@") {
			v = `\` + v
		}
		items = append(items, keySeparators.Replace(p.Key)+"=="+v)
	}
	contentType := false
	for _, h := range enabled(r.Headers) {
		contentType = contentType || strings.EqualFold(h.Key, "Content-Type")
		if h.Value == "" {
			items = append(items, keySeparators.Replace(h.Key)+";")
		} else {
			items = append(items, keySeparators.Replace(h.Key)+":"+h.Value)
		}
	}
	switch r.BodyType {
	case bodyJSON, bodyText:
		if r.Body != "" {
			opts = append(opts, "--raw="+r.Body)
		}
		// --raw sends JSON unless told otherwise.
		if r.BodyType == bodyText && !contentType {
			items = append(items, "Content-Type:text/plain; charset=utf-8")
		}
	case bodyForm, bodyMultipart:
		flag := "--form"
		if r.BodyType == bodyMultipart {
			flag = "--multipart"
		}
		opts = append(opts, flag)
		for _, f := range enabled(r.Form) {
			sep := "="
			if f.Kind == kindFile {
				sep = "@"
			}
			items = append(items, keySeparators.Replace(f.Key)+sep+f.Value)
		}
	case bodyFile:
		if r.BodyFile != "" {
			items = append(items, "@"+r.BodyFile)
		}
	}
	positional := append([]string{r.Method, r.URL}, items...)
	if slices.ContainsFunc(positional, func(s string) bool { return strings.HasPrefix(s, "-") }) {
		opts = append(opts, "--")
	}
	return append(opts, positional...)
}

var shellSafe = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

func command(args []string) string {
	parts := []string{"http"}
	for _, a := range args {
		if !shellSafe.MatchString(a) {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// recorder keeps each request of an exchange as sent, and the responses it
// shows: the redirects, then the last one.
type recorder struct {
	out     *core.Output
	buf     bytes.Buffer
	sent    []string
	hops    []hop
	headers []core.Header
}

// Request prints r now, as the exchange changes it in place for the next one.
func (rec *recorder) Request(r *core.Request, _ bool) error {
	rec.buf.Reset()
	rec.out.WriteRequest(r, "H")
	head := rec.buf.Len()
	// WriteRequest leaves out the bodies core streams, as files.
	if rec.out.WriteRequest(r, "B") == nil && rec.buf.Len() == head && r.ContentLength > 0 {
		fmt.Fprintf(&rec.buf, "<流式 Body，%s，未显示>", formatSize(int(r.ContentLength)))
	}
	rec.sent = append(rec.sent, strings.TrimRight(strings.ReplaceAll(rec.buf.String(), "\r\n", "\n"), "\n"))
	return nil
}

func (rec *recorder) Sent(*core.Request) error { return nil }
func (rec *recorder) Response(r *http.Response, headers []core.Header, _ time.Time) error {
	rec.hops = append(rec.hops, hop{r.Status, r.StatusCode, r.Header.Get("Location")})
	rec.headers = headers
	return nil
}

// send sends the request of args with the cookies of jar; the response has
// the jar as the exchange left it. A body of events streams to progress,
// as streamBody tells. It returns nil once ctx is canceled, unless a body
// was streaming.
func send(ctx context.Context, args []string, jar []core.Cookie, progress func(response, string)) *response {
	res, err := exchange(ctx, args, jar, progress)
	if err != nil && ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return &response{Err: err.Error()}
	}
	return res
}

func exchange(ctx context.Context, args []string, jar []core.Cookie, progress func(response, string)) (*response, error) {
	o, err := core.ParseOptions(append(slices.Clone(sendFlags), args...))
	if err != nil {
		return nil, err
	}
	// --pretty=format would sort the requests' headers and JSON keys.
	plain, err := core.ParseOptions(append([]string{"--pretty=none"}, args...))
	if err != nil {
		return nil, err
	}
	rec := &recorder{}
	if rec.out, err = core.NewOutput(&rec.buf, false, plain); err != nil {
		return nil, err
	}
	ex, err := core.Prepare(o, "http", nil, true)
	if err != nil {
		return nil, err
	}
	defer ex.Close()
	ex.SetCookies(jar)
	if err = ex.Open(); err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := ex.Do(ctx, rec)
	if err != nil {
		return nil, err
	}
	res := &response{
		Status:    resp.Status,
		Code:      resp.StatusCode,
		Headers:   rec.headers,
		Redirects: rec.hops[:len(rec.hops)-1],
		Sent:      strings.Join(rec.sent, "\n\n"),
		Cookies:   ex.Cookies(),
		At:        start,
	}
	if media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); media == "text/event-stream" {
		streamBody(ctx, o, resp, res, progress)
		ex.Close()
		resp.Body.Close()
		return res, nil
	}
	body, closeBody, decodeErr := core.DecodedBody(resp)
	var data []byte
	if decodeErr == nil {
		data, err = io.ReadAll(io.LimitReader(body, maxBody+1))
		closeBody()
	}
	ex.Close()
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Size, res.Duration = len(data), time.Since(start)
	if decodeErr != nil {
		res.Body = decodeErr.Error()
		return res, nil
	}
	if len(data) > maxBody {
		data, res.Truncated = data[:maxBody], true
	}
	res.Body, res.JSON = displayBody(o, resp, rec.headers, data)
	return res, nil
}

// streamBody reads a body of events as it comes, formatted line by line
// as the http command does. progress has res first, then, every
// streamEvery, res with its size and duration so far and the text shown
// since. A body cut short keeps what came, with why in Err.
func streamBody(ctx context.Context, o core.Options, resp *http.Response, res *response, progress func(response, string)) {
	res.buildViews()
	progress(*res, "")
	s := &stream{}
	flush := func() {
		var text string
		text, res.Size = s.take()
		res.Duration = time.Since(res.At)
		progress(*res, text)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(streamEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				flush()
			case <-stop:
				return
			}
		}
	})
	// DecodedBody waits for the first event.
	body, closeBody, err := core.DecodedBody(resp)
	if err == nil {
		s.body = body
		var printer *core.Output
		if printer, err = core.NewOutput(s, true, o); err == nil {
			err = printer.WriteResponse(decoded(resp, s), res.Headers, "b", time.Time{})
		}
		closeBody()
	}
	close(stop)
	wg.Wait()
	flush()
	res.Body, res.Truncated = string(s.text), s.size >= maxBody
	switch {
	case ctx.Err() != nil:
		res.Err = "已取消，只显示取消前收到的内容"
	case err != nil:
		res.Err = "接收中断：" + err.Error()
	}
}

// stream passes a body to core up to maxBody, and keeps the text core
// writes of it for streamBody to take as it comes.
type stream struct {
	body  io.Reader
	mu    sync.Mutex
	size  int
	text  []byte
	taken int
}

func (s *stream) Read(p []byte) (int, error) {
	if s.size >= maxBody {
		return 0, io.EOF
	}
	n, err := s.body.Read(p[:min(len(p), maxBody-s.size)])
	s.mu.Lock()
	s.size += n
	s.mu.Unlock()
	return n, err
}

func (s *stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.text = append(s.text, p...)
	s.mu.Unlock()
	return len(p), nil
}

// take returns the text written since it last did, and the size of the
// body read so far.
func (s *stream) take() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := string(s.text[s.taken:])
	s.taken = len(s.text)
	return text, s.size
}

// decoded returns resp with body, decoded, for its body.
func decoded(resp *http.Response, body io.Reader) *http.Response {
	d := *resp
	d.Header = resp.Header.Clone()
	d.Header.Del("Content-Encoding")
	d.Body = io.NopCloser(body)
	return &d
}

// displayBody formats a decoded response body. JSON is only re-indented,
// keeping its key order and literals as the server wrote them; anything
// else is formatted as the http command does with --pretty=format.
func displayBody(o core.Options, resp *http.Response, headers []core.Header, plain []byte) (string, bool) {
	if bytes.IndexByte(plain, 0) >= 0 {
		return fmt.Sprintf("<二进制内容，%s>", formatSize(len(plain))), false
	}
	// Non-UTF-8 text is left to core, which decodes its charset.
	var indented bytes.Buffer
	if utf8.Valid(plain) && json.Indent(&indented, bytes.TrimSpace(plain), "", "  ") == nil {
		return indented.String(), true
	}
	var out bytes.Buffer
	printer, err := core.NewOutput(&out, true, o)
	if err == nil {
		err = printer.WriteResponse(decoded(resp, bytes.NewReader(plain)), headers, "b", time.Time{})
	}
	if err != nil {
		return string(plain), false
	}
	text := strings.TrimRight(out.String(), "\n")
	return text, json.Valid([]byte(text))
}

// ago tells how long ago t was, if a minute or more.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return t.Format("1月2日 15:04")
}

func formatSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
