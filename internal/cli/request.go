package cli

import (
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

type header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type request struct {
	*http.Request
	headers      []header
	removed      map[string]bool
	defaultHost  bool
	streamedBody bool
	liveInput    bool
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
	if u.User != nil {
		authority := strings.SplitN(rawURL, "://", 2)[1]
		if end := strings.IndexAny(authority, "/?#"); end >= 0 {
			authority = authority[:end]
		}
		userinfo := authority[:strings.LastIndex(authority, "@")]
		username, password, _ := strings.Cut(userinfo, ":")
		u.User = url.UserPassword(username, password)
	}
	if u.Host == "" {
		return fail(fmt.Errorf("invalid URL: %s", rawURL))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fail(fmt.Errorf("unsupported URL scheme: %s", u.Scheme))
	}
	if !strings.Contains(u.Hostname(), ":") {
		host, err := idna.Lookup.ToASCII(u.Hostname())
		if err != nil {
			return fail(err)
		}
		if u.Port() != "" {
			host += ":" + u.Port()
		}
		u.Host = host
	}
	u.RawQuery = quoteURI(u.RawQuery)
	if u.Path == "" {
		u.Path = "/"
	}
	if !o.has("path-as-is") {
		escaped := u.EscapedPath()
		trailing := strings.HasSuffix(escaped, "/") || strings.HasSuffix(escaped, "/.") || strings.HasSuffix(escaped, "/..")
		escaped = removeDotSegments(escaped)
		if trailing && !strings.HasSuffix(escaped, "/") {
			escaped += "/"
		}
		escaped = quoteURI(escaped)
		u.Path, _ = url.PathUnescape(escaped)
		u.RawPath = escaped
	}
	var data any
	var fields, query []item
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
			if it.key == "" || strings.ContainsAny(it.key, ":\r\n") || strings.ContainsAny(it.key[:1], " \t\v\f") || strings.ContainsAny(it.value, "\r\n") {
				return fail(fmt.Errorf("invalid HTTP header: %s", it.key))
			}
			if it.sep == ";" && it.value != "" {
				return fail(fmt.Errorf("to specify an empty header use Header;"))
			}
			key := strings.ToLower(it.key)
			if it.value == "" && it.sep == ":" {
				removed[key] = true
				custom = slices.DeleteFunc(custom, func(h header) bool { return strings.EqualFold(h.Name, it.key) })
				continue
			}
			if strings.ContainsAny(it.key+it.value, "\r\n") {
				return fail(fmt.Errorf("invalid newline in header"))
			}
			delete(removed, key)
			custom = append(custom, header{it.key, strings.TrimSpace(it.value)})
		case "==":
			query = append(query, it)
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
			if ct := fileMIME(filename); ct != "" {
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
					if it.value == "NaN" {
						it.value = "nan"
					}
					if it.value == "Infinity" {
						it.value = "inf"
					}
					if it.value == "-Infinity" {
						it.value = "-inf"
					}
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
	for _, it := range groupItems(query) {
		if u.RawQuery != "" {
			u.RawQuery += "&"
		}
		u.RawQuery += url.QueryEscape(it.key) + "=" + url.QueryEscape(it.value)
	}
	fields = groupItems(fields)
	fromStdin := !stdinTTY && !o.has("ignore-stdin")
	if (fromStdin && o.has("raw")) || ((fromStdin || o.has("raw") || fileCount > 0 && !form) && data != nil) || ((fromStdin || o.has("raw")) && (fileCount > 0 || len(fields) > 0)) {
		return fail(fmt.Errorf("request body (from stdin, --raw or a file) and request data (key=value) cannot be mixed; pass --ignore-stdin"))
	}
	var liveInput io.Reader
	explicitLength := false
	for _, h := range custom {
		explicitLength = explicitLength || strings.EqualFold(h.Name, "Content-Length") && h.Value != ""
	}
	if fromStdin && !o.has("offline") && !o.has("compress") && (o.has("chunked") || explicitLength) {
		liveInput = stdin
	} else if fromStdin {
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
	jsonMode := o.value("request-type") == "json" || (!form && (data != nil || fromStdin || fileCount > 0 || o.value("raw") != ""))
	if data != nil {
		body = bodySource{{data: jsonText(data, -1, true)}}
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
			delete(removed, "content-type")
			for i := range custom {
				if strings.EqualFold(custom[i].Name, "Content-Type") && !strings.Contains(custom[i].Value, "boundary=") {
					if custom[i].Value == "" {
						custom[i].Value = contentType
					} else {
						_, boundary, _ := strings.Cut(contentType, "boundary=")
						custom[i].Value += "; boundary=" + boundary
					}
				}
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
	if liveInput != nil {
		req.Body = io.NopCloser(liveInput)
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(liveInput), nil }
	} else {
		req.GetBody = body.open
	}
	req.URL = u
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
		if !o.has("offline") {
			r.removed["content-length"] = true
		}
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
	r.defaultHost = !seen["host"] && !removed["host"]
	r.liveInput = liveInput != nil
	r.streamedBody = !o.has("offline") && !compressed && (r.liveInput || fileCount > 0 || form && o.value("request-type") == "multipart" || o.has("chunked"))
	return r, cleanup, nil
}

func removeDotSegments(path string) string {
	var parts []string
	for _, part := range strings.Split(path, "/") {
		switch part {
		case ".":
		case "..":
			if len(parts) > 1 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	result := strings.Join(parts, "/")
	if result == "" {
		return "/"
	}
	return result
}

func quoteURI(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '%' && i+2 < len(value) {
			if n, err := strconv.ParseUint(value[i+1:i+3], 16, 8); err == nil {
				if strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~", rune(n)) {
					out.WriteByte(byte(n))
				} else {
					out.WriteString(strings.ToUpper(value[i : i+3]))
				}
				i += 2
				continue
			}
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$&'()*+,-./:;=?@[]_~", rune(c)) {
			out.WriteByte(c)
		} else {
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

func (r *request) moveHeaderBefore(name, before string) {
	if strings.EqualFold(name, before) {
		return
	}
	index := -1
	for i := len(r.headers) - 1; i >= 0; i-- {
		if strings.EqualFold(r.headers[i].Name, name) {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	for i, h := range r.headers {
		if strings.EqualFold(h.Name, before) && i < index {
			value := r.headers[index]
			copy(r.headers[i+1:index+1], r.headers[i:index])
			r.headers[i] = value
			return
		}
	}
}
