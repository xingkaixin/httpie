package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"

	"golang.org/x/term"
)

func Run(program string, args []string) int {
	if len(args) == 1 && args[0] == "--internal-check-updates" {
		path, _ := updateSettings()
		defer os.Remove(path + ".lock")
		if fetchUpdates() != nil {
			return 1
		}
		return 0
	}
	code, err := run(program, args, os.Stdin, os.Stdout, os.Stderr, term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())), term.IsTerminal(int(os.Stderr.Fd())))
	if err != nil {
		if errors.Is(err, syscall.EPIPE) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "\n%s: error: %s\n", program, err)
	}
	return code
}
func run(program string, args []string, stdin io.Reader, stdout, stderr io.Writer, stdinTTY, stdoutTTY, stderrTTY bool) (code int, err error) {
	if program == "httpie" {
		return runManager(args, stdout, stderr)
	}
	configured, configErr := configArgs(args)
	if configErr != nil {
		var syntax *json.SyntaxError
		var fileErr *os.PathError
		if !errors.As(configErr, &syntax) && !errors.As(configErr, &fileErr) {
			return 1, configErr
		}
		fmt.Fprintf(stderr, "\n%s: warning: %s\n", program, configErr)
	} else {
		args = configured
	}
	o, err := parseOptions(args)
	if err != nil {
		return 1, err
	}
	if o.has("debug") || o.has("traceback") {
		defer func() {
			if err != nil {
				_, _ = stderr.Write(debug.Stack())
			}
		}()
	}
	if o.has("version") {
		_, err = fmt.Fprintln(stdout, version)
		return 0, err
	}
	if o.has("manual") {
		return 0, printManual(program, stdin, stdout, stderr, stdinTTY && stdoutTTY)
	}
	if o.has("help") {
		_, err = io.WriteString(stdout, formatHelp(helpWidth(stdout)))
		return 0, err
	}
	if o.has("debug") {
		fmt.Fprintf(stderr, "HTTPie %s\nGo %s\nPlatform %s/%s\nConfig directory: %s\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH, configDir())
	}
	if o.has("debug") && len(o.args) == 0 {
		return 0, nil
	}
	if o.has("offline") {
		delete(o.values, "download")
		delete(o.values, "continue")
	}
	writer, tty := stdout, stdoutTTY
	var outputFile *os.File
	if o.has("download") {
		writer = stderr
		tty = stderrTTY
	} else if o.has("output") {
		if o.value("output") == "-" {
			var ok bool
			outputFile, ok = stdout.(*os.File)
			if !ok {
				return 1, fmt.Errorf("standard output is not seekable")
			}
			if _, err = outputFile.Seek(0, io.SeekStart); err == nil {
				err = outputFile.Truncate(0)
			}
		} else {
			outputFile, err = os.OpenFile(expandPath(o.value("output")), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
		}
		if err != nil {
			return 1, err
		}
		defer outputFile.Close()
		writer = outputFile
		tty = false
	}
	if o.has("quiet") && !(o.has("output") && !o.has("download")) {
		writer = io.Discard
	}
	out, err := newOutput(writer, tty, o)
	if err != nil {
		return 1, err
	}
	if !stdinTTY && !o.has("ignore-stdin") && !o.has("offline") {
		var stop func()
		stdin, stop = watchStdin(stdin, stderr)
		defer stop()
	}
	ex, err := prepare(o, program, stdin, stdinTTY)
	if err != nil {
		return 1, err
	}
	defer ex.close()
	checkUpdateWarnings(stderr, program, stdoutTTY && o.has("quiet"))
	if err = ex.open(); err != nil {
		return 1, err
	}
	if o.has("offline") {
		err = out.writeRequest(ex.request, out.selection())
		if err == nil {
			err = ex.saveSession()
		}
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
	var resume int64
	if o.has("download") {
		resume, err = prepareDownload(ex.request, o)
		if err != nil {
			return 1, err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopInput := context.AfterFunc(ctx, func() {
		if closer, ok := stdin.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stopInput()
	initialURL := ex.request.URL
	response, err := ex.do(ctx, out)
	if err == nil {
		code, err = finish(ex, response, initialURL, out, stdout, stderr, stdoutTTY, resume)
	} else if limit := redirectLimitError(0); errors.As(err, &limit) {
		code = 6
	} else {
		code = 1
	}
	if ctx.Err() != nil {
		return 130, nil
	}
	if err == nil {
		if err = ex.saveSession(); err != nil {
			code = 1
		}
	}
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			code = 2
		}
	}
	return code, err
}

// finish handles the last response: its status for --check-status, and
// its body for --download.
func finish(ex *exchange, response *http.Response, initialURL *url.URL, out *output, stdout, stderr io.Writer, stdoutTTY bool, resume int64) (int, error) {
	o := ex.options
	code := 0
	if o.has("check-status") || o.has("download") {
		switch {
		case response.StatusCode >= 500:
			code = 5
		case response.StatusCode >= 400:
			code = 4
		case response.StatusCode >= 300 && !o.has("follow") && !o.has("download"):
			code = 3
		}
	}
	if code == 0 && o.has("download") {
		if err := download(response, initialURL, o, stdout, stderr, stdoutTTY, out.tty, resume); err != nil {
			ex.close()
			response.Body.Close()
			return 1, err
		}
	}
	ex.close()
	response.Body.Close()
	if code != 0 && !stdoutTTY && o.count("quiet") < 2 {
		fmt.Fprintf(stderr, "\nhttp: warning: HTTP %s\n", response.Status)
	}
	return code, nil
}
