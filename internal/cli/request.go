package cli

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type bodyPart struct {
	data []byte
	path string
}
type bodySource []bodyPart

type bodyReader struct {
	io.Reader
	files []*os.File
}

func (r *bodyReader) Close() error {
	var result error
	for _, f := range r.files {
		if err := f.Close(); err != nil {
			result = err
		}
	}
	return result
}
func (b bodySource) open() (io.ReadCloser, error) {
	r := &bodyReader{}
	var readers []io.Reader
	for _, p := range b {
		if p.path != "" {
			f, err := os.Open(p.path)
			if err != nil {
				r.Close()
				return nil, err
			}
			r.files = append(r.files, f)
			readers = append(readers, f)
		} else {
			readers = append(readers, bytes.NewReader(p.data))
		}
	}
	r.Reader = io.MultiReader(readers...)
	return r, nil
}
func (b bodySource) size() (int64, error) {
	var n int64
	for _, p := range b {
		if p.path == "" {
			n += int64(len(p.data))
		} else {
			s, err := os.Stat(p.path)
			if err != nil {
				return 0, err
			}
			n += s.Size()
		}
	}
	return n, nil
}

type header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type request struct {
	*http.Request
	headers []header
	removed map[string]bool
}

func (r *request) setHeader(name, value string) {
	for i := 0; i < len(r.headers); {
		if strings.EqualFold(r.headers[i].Name, name) {
			r.headers = append(r.headers[:i], r.headers[i+1:]...)
		} else {
			i++
		}
	}
	r.headers = append(r.headers, header{name, value})
	r.Header.Set(name, value)
	if strings.EqualFold(name, "Host") {
		r.Host = value
	}
}

var methodPattern = regexp.MustCompile(`^[a-zA-Z]+$`)
var schemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

func buildRequest(o options, program string, stdin io.Reader, stdinTTY bool) (*request, func(), error) {
	var temps []string
	cleanup := func() {
		for _, p := range temps {
			_ = os.Remove(p)
		}
	}
	fail := func(err error) (*request, func(), error) { cleanup(); return nil, func() {}, err }
	if len(o.args) == 0 {
		return fail(fmt.Errorf("URL is required"))
	}
	method, rawURL := "", o.args[0]
	rawItems := o.args[1:]
	if len(o.args) > 1 && methodPattern.MatchString(o.args[0]) {
		method = strings.ToUpper(o.args[0])
		rawURL = o.args[1]
		rawItems = o.args[2:]
	}
	rawURL = strings.TrimPrefix(rawURL, "://")
	if !schemePattern.MatchString(rawURL) {
		scheme := o.value("default-scheme")
		if program == "https" {
			scheme = "https"
		}
		if strings.HasPrefix(rawURL, ":") {
			rawURL = "localhost" + rawURL
			if strings.HasPrefix(rawURL, "localhost:/") {
				rawURL = strings.Replace(rawURL, "localhost:/", "localhost/", 1)
			}
			if rawURL == "localhost:" {
				rawURL = "localhost"
			}
		}
		rawURL = scheme + "://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fail(err)
	}
	if u.Host == "" {
		return fail(fmt.Errorf("invalid URL: %s", rawURL))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fail(fmt.Errorf("unsupported URL scheme: %s", u.Scheme))
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if !o.has("path-as-is") {
		trailing := strings.HasSuffix(u.Path, "/") || strings.HasSuffix(u.Path, "/.") || strings.HasSuffix(u.Path, "/..")
		u.Path = path.Clean(u.Path)
		if trailing && u.Path != "/" {
			u.Path += "/"
		}
		u.RawPath = ""
	}
	var data any
	var fields []item
	var custom []header
	removed := map[string]bool{}
	var body bodySource
	hasData := false
	fileCount := 0
	form := o.value("request-type") == "form" || o.value("request-type") == "multipart"
	for _, raw := range rawItems {
		it, err := parseItem(raw)
		if err != nil {
			return fail(err)
		}
		if strings.HasSuffix(it.sep, "@") && it.sep != "@" {
			it.value, err = embeddedText(it.value)
			if err != nil {
				return fail(err)
			}
			if it.sep == ":@" || it.sep == "==@" {
				it.value = strings.TrimRight(it.value, "\n")
			}
			it.sep = strings.TrimSuffix(it.sep, "@")
		}
		switch it.sep {
		case ":", ";":
			if it.sep == ";" && it.value != "" {
				return fail(fmt.Errorf("to specify an empty header use Header;"))
			}
			key := strings.ToLower(it.key)
			if it.value == "" && it.sep == ":" {
				removed[key] = true
				continue
			}
			if strings.ContainsAny(it.key+it.value, "\r\n") {
				return fail(fmt.Errorf("invalid newline in header"))
			}
			custom = append(custom, header{it.key, strings.TrimSpace(it.value)})
		case "==":
			if u.RawQuery != "" {
				u.RawQuery += "&"
			}
			u.RawQuery += url.QueryEscape(it.key) + "=" + url.QueryEscape(it.value)
		case "@":
			hasData = true
			fileCount++
			if form {
				fields = append(fields, it)
				continue
			}
			if it.key != "" || fileCount > 1 {
				return fail(fmt.Errorf("invalid file fields (perhaps you meant --form?)"))
			}
			filename, _, _ := strings.Cut(it.value, ";type=")
			filename = expandPath(filename)
			body = bodySource{{path: filename}}
			if ct := mime.TypeByExtension(filepath.Ext(filename)); ct != "" {
				custom = append(custom, header{"Content-Type", ct})
			}
		case "=", ":=":
			hasData = true
			var value any = it.value
			if it.sep == ":=" {
				value, err = decodeJSON([]byte(it.value))
				if err != nil {
					return fail(fmt.Errorf("%s: %w", raw, err))
				}
			}
			if form {
				switch v := value.(type) {
				case string:
					it.value = v
				case bool:
					if v {
						it.value = "True"
					} else {
						it.value = "False"
					}
				case object, []any, nil:
					return fail(fmt.Errorf("cannot use complex JSON value types with --form/--multipart"))
				default:
					it.value = fmt.Sprint(v)
				}
				fields = append(fields, it)
				continue
			}
			parts, err := jsonPath(it.key)
			if err != nil {
				return fail(err)
			}
			data, err = setJSON(data, parts, value)
			if err != nil {
				return fail(fmt.Errorf("%s: %w", it.key, err))
			}
		}
	}
	fromStdin := !stdinTTY && !o.has("ignore-stdin")
	if (fromStdin && o.has("raw")) || ((fromStdin || o.has("raw") || fileCount > 0 && !form) && data != nil) || ((fromStdin || o.has("raw")) && (fileCount > 0 || len(fields) > 0)) {
		return fail(fmt.Errorf("request body (from stdin, --raw or a file) and request data (key=value) cannot be mixed; pass --ignore-stdin"))
	}
	if fromStdin {
		f, err := os.CreateTemp("", "httpie-stdin-*")
		if err != nil {
			return fail(err)
		}
		temps = append(temps, f.Name())
		_, err = io.Copy(f, stdin)
		closeErr := f.Close()
		if err != nil {
			return fail(err)
		}
		if closeErr != nil {
			return fail(closeErr)
		}
		body = bodySource{{path: f.Name()}}
	}
	if o.has("raw") {
		body = bodySource{{data: []byte(o.value("raw"))}}
	}
	if method == "" {
		method = "GET"
		if hasData || fromStdin || o.has("raw") {
			method = "POST"
		}
	}
	contentType := ""
	jsonMode := o.value("request-type") == "json" || (!form && data != nil)
	if data != nil {
		body = bodySource{{data: jsonText(data, -1, false, true)}}
	}
	if jsonMode {
		contentType = "application/json"
	}
	if form {
		contentType = "application/x-www-form-urlencoded; charset=utf-8"
		if fileCount > 0 || o.value("request-type") == "multipart" {
			body, contentType, err = multipartBody(fields, o.value("boundary"))
			if err != nil {
				return fail(err)
			}
		} else if len(fields) > 0 {
			var pairs []string
			for _, it := range fields {
				pairs = append(pairs, url.QueryEscape(it.key)+"="+url.QueryEscape(it.value))
			}
			body = bodySource{{data: []byte(strings.Join(pairs, "&"))}}
		}
	}
	size, err := body.size()
	if err != nil {
		return fail(err)
	}
	compressed := false
	if o.has("compress") && size > 0 {
		input, err := body.open()
		if err != nil {
			return fail(err)
		}
		f, err := os.CreateTemp("", "httpie-deflate-*")
		if err != nil {
			input.Close()
			return fail(err)
		}
		temps = append(temps, f.Name())
		z := zlib.NewWriter(f)
		_, err = io.Copy(z, input)
		input.Close()
		zerr := z.Close()
		stat, serr := f.Stat()
		ferr := f.Close()
		for _, e := range []error{err, zerr, serr, ferr} {
			if e != nil {
				return fail(e)
			}
		}
		if stat.Size() < size || o.count("compress") > 1 {
			body = bodySource{{path: f.Name()}}
			size = stat.Size()
			compressed = true
		}
	}
	var reader io.ReadCloser
	if size > 0 {
		reader, err = body.open()
		if err != nil {
			return fail(err)
		}
	}
	req, err := http.NewRequest(method, u.String(), reader)
	if err != nil {
		if reader != nil {
			reader.Close()
		}
		return fail(err)
	}
	req.GetBody = body.open
	req.ContentLength = size
	r := &request{Request: req, removed: removed}
	r.setHeader("Accept-Encoding", "gzip, deflate, br")
	r.setHeader("Accept", "*/*")
	r.setHeader("Connection", "keep-alive")
	if size > 0 || method == "POST" || method == "PUT" || method == "PATCH" {
		r.setHeader("Content-Length", strconv.FormatInt(size, 10))
	}
	r.setHeader("User-Agent", "HTTPie/"+version)
	if jsonMode {
		r.setHeader("Accept", "application/json, */*;q=0.5")
	}
	if contentType != "" {
		r.setHeader("Content-Type", contentType)
	}
	seen := map[string]bool{}
	for _, h := range custom {
		key := strings.ToLower(h.Name)
		if !seen[key] {
			r.setHeader(h.Name, h.Value)
			seen[key] = true
		} else {
			r.headers = append(r.headers, h)
			r.Header.Add(h.Name, h.Value)
		}
	}
	if compressed {
		r.setHeader("Content-Encoding", "deflate")
		r.setHeader("Content-Length", strconv.FormatInt(size, 10))
	}
	if o.has("chunked") {
		r.removed["content-length"] = true
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
		r.setHeader("Transfer-Encoding", "chunked")
	}
	for key := range removed {
		r.Header.Del(key)
		if key == "user-agent" {
			r.Header["User-Agent"] = nil
		}
	}
	if !seen["host"] {
		r.setHeader("Host", u.Host)
	}
	for i := 0; i < len(r.headers); {
		if removed[strings.ToLower(r.headers[i].Name)] {
			r.headers = append(r.headers[:i], r.headers[i+1:]...)
		} else {
			i++
		}
	}
	return r, cleanup, nil
}

func multipartBody(fields []item, boundary string) (bodySource, string, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if boundary != "" {
		if err := w.SetBoundary(boundary); err != nil {
			return nil, "", err
		}
	}
	var body bodySource
	for _, it := range fields {
		h := textproto.MIMEHeader{}
		if it.sep == "@" {
			filename, ct, _ := strings.Cut(it.value, ";type=")
			filename = expandPath(filename)
			if ct == "" {
				ct = mime.TypeByExtension(filepath.Ext(filename))
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": it.key, "filename": filepath.Base(filename)}))
			h.Set("Content-Type", ct)
			if _, err := w.CreatePart(h); err != nil {
				return nil, "", err
			}
			body = append(body, bodyPart{data: bytes.Clone(b.Bytes())}, bodyPart{path: filename})
			b.Reset()
		} else {
			h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": it.key}))
			part, err := w.CreatePart(h)
			if err != nil {
				return nil, "", err
			}
			_, _ = io.WriteString(part, it.value)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	body = append(body, bodyPart{data: bytes.Clone(b.Bytes())})
	return body, w.FormDataContentType(), nil
}
