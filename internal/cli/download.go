package cli

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func prepareDownload(r *request, o options) (int64, error) {
	r.setHeader("Accept-Encoding", "identity")
	if filename := o.value("output"); filename != "" && filename != "-" {
		file, err := os.OpenFile(expandPath(filename), os.O_CREATE|os.O_RDWR, 0666)
		if err != nil {
			return 0, err
		}
		file.Close()
	}
	if !o.has("continue") {
		return 0, nil
	}
	if o.value("output") == "-" {
		return 0, fmt.Errorf("cannot resume a download to standard output")
	}
	stat, err := os.Stat(expandPath(o.value("output")))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if stat.Size() > 0 {
		r.setHeader("Range", fmt.Sprintf("bytes=%d-", stat.Size()))
	}
	return stat.Size(), nil
}

var contentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)

func download(r *http.Response, initialURL *url.URL, o options, stdout, stderr io.Writer, stdoutTTY, stderrTTY bool, resume int64) (err error) {
	totalSize := r.ContentLength
	if r.StatusCode == 206 && o.has("continue") {
		match := contentRange.FindStringSubmatch(r.Header.Get("Content-Range"))
		if match == nil {
			return fmt.Errorf("invalid or missing Content-Range")
		}
		start, _ := strconv.ParseInt(match[1], 10, 64)
		end, _ := strconv.ParseInt(match[2], 10, 64)
		total, _ := strconv.ParseInt(match[3], 10, 64)
		if start != resume || end < start || (match[3] != "*" && end+1 != total) {
			return fmt.Errorf("unexpected Content-Range for requested Range")
		}
		totalSize = end + 1
	} else {
		resume = 0
	}
	filename := o.value("output")
	outputStdout := filename == "-"
	if outputStdout {
		filename = ""
	}
	writer := stdout
	var file *os.File
	if filename != "" {
		flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if resume > 0 {
			flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		file, err = os.OpenFile(expandPath(filename), flags, 0666)
	} else if stdoutTTY && !outputStdout {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
		filename = strings.TrimSpace(strings.TrimLeft(filepath.Base(params["filename"]), "."))
		if filename == "" || filename == "/" {
			filename = filepath.Base(strings.TrimRight(initialURL.EscapedPath(), "/"))
			if filename == "." || filename == "/" || filename == "" {
				filename = "index"
			}
			if !strings.Contains(filename, ".") {
				media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				exts, _ := mime.ExtensionsByType(media)
				if media == "text/plain" {
					exts = []string{".txt"}
				}
				if media == "text/html" {
					exts = []string{".html"}
				}
				if len(exts) > 0 {
					filename += exts[0]
				}
			}
		}
		base := filename
		for i := 0; ; i++ {
			suffix := ""
			if i > 0 {
				suffix = fmt.Sprintf("-%d", i)
			}
			name := base
			if len(name)+len(suffix) > 255 {
				ext := filepath.Ext(name)
				limit := 255 - len(suffix)
				if len(ext) < limit {
					name = name[:limit-len(ext)] + ext
				} else {
					name = name[:limit]
				}
			}
			filename = name + suffix
			file, err = os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
			if !os.IsExist(err) {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if file != nil {
		defer file.Close()
		writer = file
	}
	body, close, err := decodedBody(r)
	if err != nil {
		return err
	}
	defer close()
	if !o.has("quiet") {
		name := filename
		if name == "" {
			name = "<stdout>"
		}
		progress := &downloadProgress{Writer: writer, display: stderr, tty: stderrTTY, total: totalSize, downloaded: resume, started: time.Now()}
		fmt.Fprintf(stderr, "Downloading to %s\n", name)
		writer = progress
		defer func() { progress.finish(err == nil) }()
	}
	written, err := io.Copy(writer, body)
	if err != nil {
		return err
	}
	if totalSize > 0 && written+resume != totalSize {
		return fmt.Errorf("incomplete download: size=%d; downloaded=%d", totalSize, written+resume)
	}
	if file != nil {
		if err = file.Close(); err != nil {
			return err
		}
	}
	return nil
}

type downloadProgress struct {
	io.Writer
	display           io.Writer
	tty               bool
	total, downloaded int64
	started, last     time.Time
}

func (p *downloadProgress) Write(data []byte) (int, error) {
	n, err := p.Writer.Write(data)
	p.downloaded += int64(n)
	now := time.Now()
	if p.tty && now.Sub(p.last) >= 100*time.Millisecond {
		p.last = now
		speed := float64(p.downloaded) / now.Sub(p.started).Seconds()
		if p.total > 0 {
			percent := min(100.0, float64(p.downloaded)*100/float64(p.total))
			filled := int(percent / 4)
			remaining := time.Duration(float64(p.total-p.downloaded)/speed) * time.Second
			fmt.Fprintf(p.display, "\r\x1b[K[%s%s] %3.0f%% (%s / %s) %s %s/s", strings.Repeat("=", filled), strings.Repeat(" ", 25-filled), percent, decimalSize(float64(p.downloaded)), decimalSize(float64(p.total)), remaining, decimalSize(speed))
		} else {
			fmt.Fprintf(p.display, "\r\x1b[K%s / ?  %s/s", decimalSize(float64(p.downloaded)), decimalSize(speed))
		}
	}
	return n, err
}
func (p *downloadProgress) finish(success bool) {
	if p.tty {
		fmt.Fprint(p.display, "\r\x1b[K")
	}
	elapsed := time.Since(p.started).Seconds()
	verb := "Done"
	if !success {
		verb = "Interrupted"
	}
	minutes := int(elapsed) / 60
	duration := fmt.Sprintf("%02d:%08.5f", minutes%60, elapsed-float64(minutes*60))
	if minutes >= 60 {
		duration = fmt.Sprintf("%d:%s", minutes/60, duration)
	}
	fmt.Fprintf(p.display, "%s. %s in %s (%s/s)\n", verb, decimalSize(float64(p.downloaded)), duration, decimalSize(float64(p.downloaded)/elapsed))
}
func decimalSize(size float64) string {
	units := []string{"bytes", "kB", "MB", "GB", "TB", "PB"}
	unit := 0
	for size >= 1000 && unit < len(units)-1 {
		size /= 1000
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%.0f bytes", size)
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}
