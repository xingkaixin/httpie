package cli

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"golang.org/x/text/transform"
)

type output struct {
	writer  io.Writer
	tty     bool
	options options
	pretty  string
	format  map[string]string
}

func newOutput(w io.Writer, tty bool, o options) (*output, error) {
	p := o.value("pretty")
	if p == "" {
		p = "none"
		if tty {
			p = "all"
		}
	}
	f := map[string]string{"headers.sort": "true", "json.format": "true", "json.indent": "4", "json.sort_keys": "true", "xml.format": "true", "xml.indent": "2"}
	for _, group := range o.values["format-options"] {
		for _, item := range strings.Split(group, ",") {
			k, v, ok := strings.Cut(strings.ToLower(item), ":")
			if !ok {
				return nil, fmt.Errorf("invalid format option: %s", item)
			}
			if _, exists := f[k]; !exists {
				return nil, fmt.Errorf("unknown format option: %s", k)
			}
			if strings.HasSuffix(k, "indent") {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || strings.Trim(v, "0123456789") != "" {
					return nil, fmt.Errorf("invalid indent: %s", v)
				}
			} else if v != "true" && v != "false" {
				return nil, fmt.Errorf("invalid boolean: %s", v)
			}
			f[k] = v
		}
	}
	return &output{writer: w, tty: tty, options: o, pretty: p, format: f}, nil
}
func (o *output) selection() string {
	if o.options.has("print") {
		return o.options.value("print")
	}
	if o.options.count("verbose") > 1 {
		return "HBhbm"
	}
	if o.options.has("verbose") {
		return "HBhb"
	}
	if o.options.has("offline") {
		return "HB"
	}
	if o.tty {
		return "hb"
	}
	return "b"
}
func (o *output) formatting() bool { return o.pretty == "all" || o.pretty == "format" }
func (o *output) coloring() bool   { return o.pretty == "all" || o.pretty == "colors" }
func (o *output) writeHeaders(status string, headers []header) error {
	fields := append([]header(nil), headers...)
	if o.formatting() && o.format["headers.sort"] == "true" {
		sort.SliceStable(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	}
	lines := []string{status}
	for _, h := range fields {
		lines = append(lines, h.Name+": "+h.Value)
	}
	text := strings.Join(lines, "\r\n")
	if o.coloring() {
		text = strings.TrimSpace(o.color(text, "http"))
	}
	_, err := io.WriteString(o.writer, text+"\r\n\r\n")
	return err
}
func (o *output) writeRequest(r *request, selection string) error {
	if strings.Contains(selection, "H") {
		uri := r.URL.RequestURI()
		if o.options.has("path-as-is") {
			uri = r.URL.EscapedPath()
			if r.URL.RawPath != "" {
				uri = r.URL.RawPath
			}
			if r.URL.RawQuery != "" {
				uri += "?" + r.URL.RawQuery
			}
		}
		if err := o.writeHeaders(r.Method+" "+uri+" HTTP/1.1", r.headers); err != nil {
			return err
		}
	}
	if strings.Contains(selection, "B") && !r.streamedBody {
		body, err := r.GetBody()
		if err != nil {
			return err
		}
		defer body.Close()
		if r.ContentLength != 0 {
			if err = o.writeBody(body, r.Header.Get("Content-Type"), false, strings.Contains(selection, "H")); err != nil {
				return err
			}
		}
		if o.tty {
			_, err = io.WriteString(o.writer, "\n\n")
			return err
		}
	}
	return nil
}
func (o *output) writeResponse(r *http.Response, headers []header, selection string, start time.Time) error {
	if strings.Contains(selection, "h") {
		if err := o.writeHeaders(r.Proto+" "+r.Status, responseHeaders(headers)); err != nil {
			return err
		}
	}
	if strings.Contains(selection, "b") {
		body, close, err := decodedBody(r)
		if err != nil {
			return err
		}
		defer close()
		ct := r.Header.Get("Content-Type")
		if o.options.has("response-mime") {
			ct = o.options.value("response-mime")
		}
		if err = o.writeBody(body, ct, true, strings.Contains(selection, "h")); err != nil {
			return err
		}
		if o.tty && !strings.Contains(selection, "m") {
			if _, err = io.WriteString(o.writer, "\n\n"); err != nil {
				return err
			}
		}
	}
	if strings.Contains(selection, "m") {
		if strings.Contains(selection, "b") {
			if _, err := io.WriteString(o.writer, "\n\n"); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(o.writer, "Elapsed time: %ss\n\n", strconv.FormatFloat(time.Since(start).Seconds(), 'f', 10, 64))
		return err
	}
	return nil
}
func responseHeaders(raw []header) []header {
	var result, cookies []header
	for _, h := range raw {
		if strings.EqualFold(h.Name, "Set-Cookie") {
			cookies = append(cookies, h)
			continue
		}
		merged := false
		for i := range result {
			if strings.EqualFold(result[i].Name, h.Name) {
				result[i].Value += ", " + h.Value
				merged = true
				break
			}
		}
		if !merged {
			result = append(result, h)
		}
	}
	return append(result, cookies...)
}
func decodedBody(r *http.Response) (io.Reader, func(), error) {
	var reader io.Reader = r.Body
	buffer := bufio.NewReader(reader)
	if _, err := buffer.Peek(1); err == io.EOF {
		return buffer, func() {}, nil
	}
	reader = buffer
	var closers []io.Closer
	close := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			_ = closers[i].Close()
		}
	}
	encodings := strings.Split(r.Header.Get("Content-Encoding"), ",")
	for i := len(encodings) - 1; i >= 0; i-- {
		switch strings.TrimSpace(strings.ToLower(encodings[i])) {
		case "gzip", "x-gzip":
			z, err := gzip.NewReader(reader)
			if err != nil {
				close()
				return nil, func() {}, err
			}
			reader = z
			closers = append(closers, z)
		case "deflate":
			b := bufio.NewReader(reader)
			prefix, err := b.Peek(2)
			if err != nil {
				close()
				return nil, func() {}, err
			}
			var z io.ReadCloser
			// Raw deflate can pass the zlib check bits alone, so also require the deflate method and a valid window.
			if prefix[0]&0x0f == 8 && prefix[0]>>4 <= 7 && (int(prefix[0])*256+int(prefix[1]))%31 == 0 {
				z, err = zlib.NewReader(b)
			} else {
				z = flate.NewReader(b)
			}
			if err != nil {
				close()
				return nil, func() {}, err
			}
			reader = z
			closers = append(closers, z)
		case "br":
			reader = brotli.NewReader(reader)
		}
	}
	return reader, close, nil
}

const binaryNotice = "\n+-----------------------------------------+\n| NOTE: binary data not shown in terminal |\n+-----------------------------------------+"

func (o *output) writeBody(reader io.Reader, contentType string, response, headers bool) error {
	if !o.tty && o.pretty == "none" {
		_, err := io.Copy(o.writer, reader)
		return err
	}
	media, params, _ := mime.ParseMediaType(contentType)
	encoding := params["charset"]
	outputEncoding := encoding
	if response && o.options.has("response-charset") {
		encoding = o.options.value("response-charset")
	}
	notice := binaryNotice
	if headers {
		notice = "\n" + notice
	}
	process := func(data []byte) error {
		if bytes.ContainsRune(data, 0) {
			_, err := io.WriteString(o.writer, notice)
			return err
		}
		data = decodeText(data, encoding)
		if !utf8.Valid(data) {
			data = bytes.ToValidUTF8(data, []byte("�"))
		}
		if o.formatting() && validMIME(media) {
			if o.format["json.format"] == "true" && (strings.Contains(media, "json") || strings.Contains(media, "javascript") || strings.Contains(media, "text") || o.options.value("request-type") == "json") {
				indent, _ := strconv.Atoi(o.format["json.indent"])
				starts := []int{0}
				if prefix := bytes.IndexAny(data, "{[\""); prefix > 0 {
					starts = append(starts, prefix)
				}
				for _, start := range starts {
					if value, err := decodeJSON(data[start:]); err == nil {
						if o.format["json.sort_keys"] == "true" {
							if err := sortJSON(value); err != nil {
								return err
							}
						}
						data = append(bytes.Clone(data[:start]), jsonText(value, indent, false)...)
						break
					}
				}
			}
			if o.format["xml.format"] == "true" && strings.Contains(media, "xml") {
				indent, _ := strconv.Atoi(o.format["xml.indent"])
				data = formatXML(data, indent)
			}
		}
		if o.coloring() && validMIME(media) {
			data = []byte(o.color(string(data), media))
		}
		if !o.tty && outputEncoding != "" && !strings.EqualFold(outputEncoding, "utf-8") {
			if enc := textEncoding(outputEncoding); enc != nil {
				converted, _, err := transform.Bytes(enc.NewEncoder(), data)
				if err == nil {
					data = converted
				}
			}
		}
		_, err := o.writer.Write(data)
		return err
	}
	if o.options.has("stream") || media == "text/event-stream" || o.pretty == "none" {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), int(^uint(0)>>1))
		scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
			separators := "\n"
			if response {
				separators = "\r\n"
			}
			if index := bytes.IndexAny(data, separators); index >= 0 {
				if response {
					return index + 1, data[:index], nil
				}
				return index + 1, data[:index+1], nil
			}
			if atEOF && len(data) > 0 {
				return len(data), data, nil
			}
			return 0, nil, nil
		})
		for scanner.Scan() {
			line := scanner.Bytes()
			if err := process(line); err != nil {
				return err
			}
			if bytes.ContainsRune(line, 0) {
				return nil
			}
			if response {
				if _, err := io.WriteString(o.writer, "\n"); err != nil {
					return err
				}
			}
		}
		return scanner.Err()
	}
	var data bytes.Buffer
	buffer := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if bytes.ContainsRune(buffer[:n], 0) {
				_, e := io.WriteString(o.writer, notice)
				return e
			}
			data.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return process(data.Bytes())
}

func validMIME(media string) bool {
	kind, subtype, ok := strings.Cut(media, "/")
	return ok && kind != "" && subtype != "" && !strings.Contains(subtype, "/")
}

type displayedUpload struct {
	io.ReadCloser
	output      *output
	contentType string
	headers     bool
	lines       bool
	pending     []byte
}

func (r *displayedUpload) Read(data []byte) (int, error) {
	n, err := r.ReadCloser.Read(data)
	if r.lines {
		r.pending = append(r.pending, data[:n]...)
		for len(r.pending) > 0 {
			end := bytes.IndexByte(r.pending, '\n') + 1
			if end == 0 {
				if err != io.EOF {
					break
				}
				end = len(r.pending)
			}
			if writeErr := r.output.writeBody(bytes.NewReader(r.pending[:end]), r.contentType, false, r.headers); writeErr != nil {
				return n, writeErr
			}
			r.pending = r.pending[end:]
		}
	} else if n > 0 {
		if writeErr := r.output.writeBody(bytes.NewReader(data[:n]), r.contentType, false, r.headers); writeErr != nil {
			return n, writeErr
		}
	}
	return n, err
}
