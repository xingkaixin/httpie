package core

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
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
	if len(b) == 1 && b[0].path != "" {
		return os.Open(b[0].path)
	}
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
			filename = ExpandPath(filename)
			if ct == "" {
				ct = fileMIME(filename)
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, multipartQuote(it.key), multipartQuote(filepath.Base(filename))))
			h.Set("Content-Type", ct)
			if _, err := w.CreatePart(h); err != nil {
				return nil, "", err
			}
			body = append(body, bodyPart{data: bytes.Clone(b.Bytes())}, bodyPart{path: filename})
			b.Reset()
		} else {
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, multipartQuote(it.key)))
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
	if len(fields) == 0 {
		return bodySource{{data: bytes.TrimPrefix(b.Bytes(), []byte("\r\n"))}}, w.FormDataContentType(), nil
	}
	body = append(body, bodyPart{data: bytes.Clone(b.Bytes())})
	return body, w.FormDataContentType(), nil
}

func fileMIME(filename string) string {
	value, _, _ := strings.Cut(mime.TypeByExtension(filepath.Ext(filename)), ";")
	return value
}

func multipartQuote(value string) string {
	return strings.NewReplacer("\r", "%0D", "\n", "%0A", `"`, "%22").Replace(value)
}
