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
	Err       string
}

// sendFlags go with every request the app sends, but not into the command
// it copies: --all reports redirects, --pretty=format formats the body.
var sendFlags = []string{"--all", "--pretty=format"}

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
	case bodyForm:
		opts = append(opts, "--form")
		for _, f := range enabled(r.Form) {
			items = append(items, keySeparators.Replace(f.Key)+"="+f.Value)
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

// recorder keeps the responses an exchange shows: the redirects, then the
// last one.
type recorder struct {
	hops    []hop
	headers []core.Header
}

func (rec *recorder) Request(*core.Request, bool) error { return nil }
func (rec *recorder) Sent(*core.Request) error          { return nil }
func (rec *recorder) Response(r *http.Response, headers []core.Header, _ time.Time) error {
	rec.hops = append(rec.hops, hop{r.Status, r.StatusCode, r.Header.Get("Location")})
	rec.headers = headers
	return nil
}

func send(ctx context.Context, args []string) *response {
	res, err := exchange(ctx, args)
	if ctx.Err() != nil {
		return &response{Err: "已取消"}
	}
	if err != nil {
		return &response{Err: err.Error()}
	}
	return res
}

func exchange(ctx context.Context, args []string) (*response, error) {
	o, err := core.ParseOptions(append(slices.Clone(sendFlags), args...))
	if err != nil {
		return nil, err
	}
	ex, err := core.Prepare(o, "http", nil, true)
	if err != nil {
		return nil, err
	}
	defer ex.Close()
	if err = ex.Open(); err != nil {
		return nil, err
	}
	start := time.Now()
	rec := &recorder{}
	resp, err := ex.Do(ctx, rec)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
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
	}
	if len(data) > maxBody {
		data, res.Truncated = data[:maxBody], true
	}
	res.Body, res.JSON = displayBody(o, resp, rec.headers, data)
	return res, nil
}

// displayBody formats a response body as the http command does with
// --pretty=format.
func displayBody(o core.Options, resp *http.Response, headers []core.Header, data []byte) (string, bool) {
	encoded := *resp
	encoded.Body = io.NopCloser(bytes.NewReader(data))
	reader, closeBody, err := core.DecodedBody(&encoded)
	if err != nil {
		return err.Error(), false
	}
	plain, err := io.ReadAll(reader)
	closeBody()
	if err != nil {
		plain = data
	}
	if bytes.IndexByte(plain, 0) >= 0 {
		return fmt.Sprintf("<二进制内容，%s>", formatSize(len(plain))), false
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
