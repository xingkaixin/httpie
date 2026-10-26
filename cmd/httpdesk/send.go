package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const maxBody = 10 << 20

type response struct {
	Status    string
	Code      int
	Headers   [][2]string
	Body      string
	JSON      bool
	Size      int
	Truncated bool
	Duration  time.Duration
	Err       string
}

// normalizeURL fills in the scheme the way HTTPie does: ":3000/x" is localhost,
// local hosts use http, everything else https.
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.Contains(raw, "://"):
		return raw
	case strings.HasPrefix(raw, ":"):
		return "http://localhost" + raw
	case strings.HasPrefix(raw, "localhost"), strings.HasPrefix(raw, "127."), strings.HasPrefix(raw, "0.0.0.0"):
		return "http://" + raw
	}
	return "https://" + raw
}

func requestBody(r *request) (body, contentType string) {
	switch r.BodyType {
	case bodyJSON:
		return r.Body, "application/json"
	case bodyText:
		return r.Body, "text/plain; charset=utf-8"
	case bodyForm:
		vals := url.Values{}
		for _, f := range enabled(r.Form) {
			vals.Add(f.Key, f.Value)
		}
		return vals.Encode(), "application/x-www-form-urlencoded"
	}
	return "", ""
}

func requestURL(r *request) (string, error) {
	u, err := url.Parse(normalizeURL(r.URL))
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("请输入请求地址")
	}
	if params := enabled(r.Params); len(params) > 0 {
		q := u.Query()
		for _, p := range params {
			q.Add(p.Key, p.Value)
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func buildRequest(ctx context.Context, r *request) (*http.Request, error) {
	target, err := requestURL(r)
	if err != nil {
		return nil, err
	}
	body, contentType := requestBody(r)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HTTPDesk/0.1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	switch r.AuthType {
	case authBasic:
		req.SetBasicAuth(r.User, r.Password)
	case authBearer:
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	for _, h := range enabled(r.Headers) {
		req.Header.Set(h.Key, h.Value)
	}
	return req, nil
}

func do(client *http.Client, req *http.Request) *response {
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &response{Err: "已取消"}
		}
		return &response{Err: err.Error(), Duration: time.Since(start)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	res := &response{Status: resp.Status, Code: resp.StatusCode, Duration: time.Since(start), Size: len(data)}
	if err != nil {
		res.Err = err.Error()
	}
	if len(data) > maxBody {
		data, res.Truncated = data[:maxBody], true
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			res.Headers = append(res.Headers, [2]string{k, v})
		}
	}
	slices.SortFunc(res.Headers, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	res.Body, res.JSON = displayBody(resp.Header.Get("Content-Type"), data)
	return res
}

func displayBody(contentType string, data []byte) (string, bool) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasSuffix(mediaType, "json") || json.Valid(data) {
		var out bytes.Buffer
		if json.Indent(&out, data, "", "  ") == nil {
			return out.String(), true
		}
	}
	if !utf8.Valid(data) {
		return fmt.Sprintf("<%s 二进制内容，%s>", mediaType, formatSize(len(data))), false
	}
	return string(data), false
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

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func curlCommand(r *request) (string, error) {
	req, err := buildRequest(context.Background(), r)
	if err != nil {
		return "", err
	}
	parts := []string{"curl"}
	if req.Method != http.MethodGet {
		parts = append(parts, "-X", req.Method)
	}
	parts = append(parts, shellQuote(req.URL.String()))
	for _, k := range slices.Sorted(maps.Keys(req.Header)) {
		for _, v := range req.Header[k] {
			parts = append(parts, "-H", shellQuote(k+": "+v))
		}
	}
	if body, _ := requestBody(r); body != "" {
		parts = append(parts, "--data-raw", shellQuote(body))
	}
	return strings.Join(parts, " \\\n  "), nil
}
