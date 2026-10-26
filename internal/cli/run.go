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

	"httpie/internal/core"
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
	configured, configErr := core.ConfigArgs(args)
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
	o, err := core.ParseOptions(args)
	if err != nil {
		return 1, err
	}
	if o.Has("debug") || o.Has("traceback") {
		defer func() {
			if err != nil {
				_, _ = stderr.Write(debug.Stack())
			}
		}()
	}
	if o.Has("version") {
		_, err = fmt.Fprintln(stdout, core.Version)
		return 0, err
	}
	if o.Has("manual") {
		return 0, printManual(program, stdin, stdout, stderr, stdinTTY && stdoutTTY)
	}
	if o.Has("help") {
		_, err = io.WriteString(stdout, formatHelp(helpWidth(stdout)))
		return 0, err
	}
	if o.Has("debug") {
		fmt.Fprintf(stderr, "HTTPie %s\nGo %s\nPlatform %s/%s\nConfig directory: %s\n", core.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, core.ConfigDir())
	}
	if o.Has("debug") && len(o.Args) == 0 {
		return 0, nil
	}
	writer, tty := stdout, stdoutTTY
	var outputFile *os.File
	if o.Has("download") {
		writer = stderr
		tty = stderrTTY
	} else if o.Has("output") {
		if o.Value("output") == "-" {
			var ok bool
			outputFile, ok = stdout.(*os.File)
			if !ok {
				return 1, fmt.Errorf("standard output is not seekable")
			}
			if _, err = outputFile.Seek(0, io.SeekStart); err == nil {
				err = outputFile.Truncate(0)
			}
		} else {
			outputFile, err = os.OpenFile(core.ExpandPath(o.Value("output")), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
		}
		if err != nil {
			return 1, err
		}
		defer outputFile.Close()
		writer = outputFile
		tty = false
	}
	if o.Has("quiet") && !(o.Has("output") && !o.Has("download")) {
		writer = io.Discard
	}
	out, err := core.NewOutput(writer, tty, o)
	if err != nil {
		return 1, err
	}
	if !stdinTTY && !o.Has("ignore-stdin") && !o.Has("offline") {
		var stop func()
		stdin, stop = watchStdin(stdin, stderr)
		defer stop()
	}
	ex, err := core.Prepare(o, program, stdin, stdinTTY)
	if err != nil {
		return 1, err
	}
	defer ex.Close()
	checkUpdateWarnings(stderr, program, stdoutTTY && o.Has("quiet"))
	if err = ex.Open(); err != nil {
		return 1, err
	}
	if o.Has("offline") {
		err = out.WriteRequest(ex.Request, out.Selection())
		if err == nil {
			err = ex.SaveSession()
		}
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
	var resume int64
	if o.Has("download") {
		resume, err = prepareDownload(ex.Request, o)
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
	initialURL := ex.Request.URL
	response, err := ex.Do(ctx, out)
	if err == nil {
		code, err = finish(ex, o, response, initialURL, stdout, stderr, stdoutTTY, stderrTTY, resume)
	} else if limit := core.RedirectLimitError(0); errors.As(err, &limit) {
		code = 6
	} else {
		code = 1
	}
	if ctx.Err() != nil {
		return 130, nil
	}
	if err == nil {
		if err = ex.SaveSession(); err != nil {
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
func finish(ex *core.Exchange, o core.Options, response *http.Response, initialURL *url.URL, stdout, stderr io.Writer, stdoutTTY, stderrTTY bool, resume int64) (int, error) {
	code := 0
	if o.Has("check-status") || o.Has("download") {
		switch {
		case response.StatusCode >= 500:
			code = 5
		case response.StatusCode >= 400:
			code = 4
		case response.StatusCode >= 300 && !o.Has("follow") && !o.Has("download"):
			code = 3
		}
	}
	if code == 0 && o.Has("download") {
		if err := download(response, initialURL, o, stdout, stderr, stdoutTTY, stderrTTY, resume); err != nil {
			ex.Close()
			response.Body.Close()
			return 1, err
		}
	}
	ex.Close()
	response.Body.Close()
	if code != 0 && !stdoutTTY && o.Count("quiet") < 2 {
		fmt.Fprintf(stderr, "\nhttp: warning: HTTP %s\n", response.Status)
	}
	return code, nil
}
