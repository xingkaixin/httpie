package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type observedStdin struct {
	*os.File
	ready chan struct{}
	once  sync.Once
}

func (r *observedStdin) markReady() { r.once.Do(func() { close(r.ready) }) }

func (r *observedStdin) Read(data []byte) (int, error) {
	n, err := r.File.Read(data)
	if n > 0 || err != nil {
		r.markReady()
	}
	return n, err
}

func watchStdin(reader io.Reader, stderr io.Writer) (io.Reader, func()) {
	if reader != os.Stdin || runtime.GOOS == "windows" {
		return reader, func() {}
	}
	seconds := 10.0
	if value := os.Getenv("HTTPIE_STDIN_READ_WARN_THRESHOLD"); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			seconds = parsed
		}
	}
	if seconds <= 0 {
		return reader, func() {}
	}
	observed := &observedStdin{File: os.Stdin, ready: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-observed.ready:
		case <-timer.C:
			threshold := strconv.FormatFloat(seconds, 'f', -1, 64)
			if !strings.Contains(threshold, ".") {
				threshold += ".0"
			}
			fmt.Fprintf(stderr, "> warning: no stdin data read in %ss (perhaps you want to --ignore-stdin)\n> See: https://httpie.io/docs/cli/best-practices\n", threshold)
		}
	}()
	return observed, func() { observed.markReady(); <-done }
}
