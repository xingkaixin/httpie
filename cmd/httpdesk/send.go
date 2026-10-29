package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui/editor"

	"httpie/internal/core"
)

const maxBody = 10 << 20

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
	Cookies   []core.Cookie
	Err       string
	view      *editor.State
	sentView  *editor.State
}

// sendFlags go with every request the app sends, but not into the command
// it copies: --all reports redirects, --pretty=format formats the body, and
// the rest keep the server's key order and indent as displayBody does.
var sendFlags = []string{"--all", "--pretty=format", "--unsorted", "--format-options=json.indent:2"}

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
		items = append(items, keySeparators.Replace(p.Key)+"=="+p.Value)
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
	// The URL's query goes as the Params it is synced with, which core
	// escapes once their variables are filled in.
	positional := append([]string{r.Method, withQuery(r.URL, nil)}, items...)
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
// the jar as the exchange left it.
func send(ctx context.Context, args []string, jar []core.Cookie) *response {
	res, err := exchange(ctx, args, jar)
	if ctx.Err() != nil {
		return &response{Err: "已取消"}
	}
	if err != nil {
		return &response{Err: err.Error()}
	}
	return res
}

func exchange(ctx context.Context, args []string, jar []core.Cookie) (*response, error) {
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
	res := &response{
		Status:    resp.Status,
		Code:      resp.StatusCode,
		Headers:   rec.headers,
		Size:      len(data),
		Duration:  time.Since(start),
		Redirects: rec.hops[:len(rec.hops)-1],
		Sent:      strings.Join(rec.sent, "\n\n"),
		Cookies:   ex.Cookies(),
	}
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
	decoded := *resp
	decoded.Header = resp.Header.Clone()
	decoded.Header.Del("Content-Encoding")
	decoded.Body = io.NopCloser(bytes.NewReader(plain))
	var out bytes.Buffer
	printer, err := core.NewOutput(&out, true, o)
	if err == nil {
		err = printer.WriteResponse(&decoded, headers, "b", time.Time{})
	}
	if err != nil {
		return string(plain), false
	}
	text := strings.TrimRight(out.String(), "\n")
	return text, json.Valid([]byte(text))
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
