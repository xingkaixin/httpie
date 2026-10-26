package core

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const Version = "3.2.4"

type optionSpec struct {
	name, short, dest, kind, defaultValue, description string
}

var optionSpecs = []optionSpec{
	{"json", "j", "request-type", "const", "", "Serialize request data as JSON"},
	{"form", "f", "request-type", "const", "", "Serialize request data as form fields"},
	{"multipart", "", "request-type", "const", "", "Always use multipart/form-data"},
	{"boundary", "", "", "value", "", "Multipart boundary"},
	{"raw", "", "", "value", "", "Raw request body"},
	{"compress", "x", "", "count", "", "Compress request with deflate; repeat to force"},
	{"pretty", "", "", "value", "", "Output processing: all, colors, format, none"},
	{"style", "s", "", "value", "auto", "Output color style"},
	{"unsorted", "", "format-options", "const", "", "Disable output sorting"},
	{"sorted", "", "format-options", "const", "", "Enable output sorting"},
	{"no-unsorted", "", "format-options", "const", "", "Enable output sorting"},
	{"no-sorted", "", "format-options", "const", "", "Disable output sorting"},
	{"response-charset", "", "", "value", "", "Override response encoding"},
	{"response-mime", "", "", "value", "", "Override response MIME type"},
	{"format-options", "", "", "append", "", "Formatting options separated by commas"},
	{"print", "p", "", "value", "", "Output H=request headers B=request body h=response headers b=response body m=metadata"},
	{"headers", "h", "print", "const", "", "Print response headers"},
	{"meta", "m", "print", "const", "", "Print response metadata"},
	{"body", "b", "print", "const", "", "Print response body"},
	{"verbose", "v", "", "count", "", "Show request and response; repeat for metadata"},
	{"all", "", "", "bool", "", "Show intermediate exchanges"},
	{"history-print", "P", "", "value", "", "Output selection for intermediate exchanges"},
	{"stream", "S", "", "bool", "", "Stream output as it arrives"},
	{"output", "o", "", "value", "", "Write output to FILE"},
	{"download", "d", "", "bool", "", "Download response body"},
	{"continue", "c", "", "bool", "", "Resume download to an explicit output file"},
	{"quiet", "q", "", "count", "", "Suppress normal output; repeat to suppress warnings"},
	{"session", "", "", "value", "", "Read and update a session"},
	{"session-read-only", "", "", "value", "", "Read a session without updating it"},
	{"auth", "a", "", "value", "", "USER[:PASS] or bearer TOKEN"},
	{"auth-type", "A", "", "value", "", "Authentication: basic, digest, bearer"},
	{"ignore-netrc", "", "", "bool", "", "Ignore .netrc credentials"},
	{"offline", "", "", "bool", "", "Build and print the request without sending"},
	{"proxy", "", "", "append", "", "PROTOCOL:PROXY_URL"},
	{"follow", "F", "", "bool", "", "Follow redirects"},
	{"max-redirects", "", "", "int", "30", "Maximum followed redirects"},
	{"max-headers", "", "", "int", "0", "Maximum response headers; 0 is unlimited"},
	{"timeout", "", "", "float", "0", "Connection and idle timeout in seconds"},
	{"check-status", "", "", "bool", "", "Exit nonzero for HTTP errors"},
	{"path-as-is", "", "", "bool", "", "Keep URL dot segments"},
	{"chunked", "", "", "bool", "", "Use chunked request transfer"},
	{"verify", "", "", "value", "yes", "Verify TLS: yes, no, or CA bundle path"},
	{"ssl", "", "", "value", "", "TLS protocol version"},
	{"ciphers", "", "", "value", "", "OpenSSL cipher list"},
	{"cert", "", "", "value", "", "Client certificate path"},
	{"cert-key", "", "", "value", "", "Client private key path"},
	{"cert-key-pass", "", "", "value", "", "Private key passphrase"},
	{"ignore-stdin", "I", "", "bool", "", "Do not read standard input"},
	{"help", "", "", "bool", "", "Show help"},
	{"manual", "", "", "bool", "", "Show manual"},
	{"version", "", "", "bool", "", "Show version"},
	{"traceback", "", "", "bool", "", "Show diagnostics on failure"},
	{"default-scheme", "", "", "value", "http", "Default URL scheme"},
	{"debug", "", "", "bool", "", "Show diagnostic information"},
}

var negativeArgument = regexp.MustCompile(`^-([0-9]+|[0-9]*\.[0-9]+)$`)

func OptionLike(value string) bool {
	return len(value) > 1 && value[0] == '-' && !negativeArgument.MatchString(value)
}

type Options struct {
	values map[string][]string
	Args   []string
}

func (o Options) Value(name string) string {
	values := o.values[name]
	if len(values) > 0 {
		return values[len(values)-1]
	}
	for _, s := range optionSpecs {
		if s.name == name {
			return s.defaultValue
		}
	}
	return ""
}
func (o Options) Has(name string) bool    { return len(o.values[name]) > 0 }
func (o Options) Count(name string) int   { return len(o.values[name]) }
func (o Options) Integer(name string) int { n, _ := strconv.Atoi(o.Value(name)); return n }

func ParseOptions(args []string) (Options, error) {
	o := Options{values: make(map[string][]string)}
	var resets []string
	stop := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if stop || arg == "-" || !strings.HasPrefix(arg, "-") {
			o.Args = append(o.Args, arg)
			continue
		}
		if arg == "--" {
			stop = true
			continue
		}
		long := strings.HasPrefix(arg, "--")
		name, value, supplied := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !long {
			name = arg[1:2]
			value = strings.TrimPrefix(arg[2:], "=")
			supplied = value != ""
		}
		var spec *optionSpec
		for j := range optionSpecs {
			s := &optionSpecs[j]
			if (long && s.name == name) || (!long && s.short == name) {
				spec = s
				break
			}
		}
		if spec == nil && long && strings.HasPrefix(name, "no-") && !supplied {
			for _, s := range optionSpecs {
				if s.name == name[3:] {
					dest := s.dest
					if dest == "" {
						dest = s.name
					}
					resets = append(resets, dest)
					spec = &s
					break
				}
			}
			if spec != nil {
				continue
			}
		}
		if spec == nil && long {
			for j := range optionSpecs {
				s := &optionSpecs[j]
				if strings.HasPrefix(s.name, name) {
					if spec != nil {
						return o, fmt.Errorf("ambiguous option: --%s", name)
					}
					spec = s
				}
			}
		}
		if spec == nil {
			return o, fmt.Errorf("unrecognized arguments: %s", arg)
		}
		switch spec.kind {
		case "value", "append", "int", "float":
			if !supplied {
				if i+1 < len(args) && !OptionLike(args[i+1]) {
					i++
					value = args[i]

				} else {
					return o, fmt.Errorf("argument --%s: expected one argument", spec.name)
				}
			}
			if spec.kind == "int" {
				if _, err := strconv.Atoi(value); err != nil {
					return o, fmt.Errorf("invalid integer for --%s: %s", spec.name, value)
				}
			}
			if spec.kind == "float" {
				if _, err := strconv.ParseFloat(value, 64); err != nil {
					return o, fmt.Errorf("invalid timeout: %s", value)
				}
			}
			if err := validateOption(spec.name, value); err != nil {
				return o, err
			}
		default:
			if supplied && long {
				return o, fmt.Errorf("argument --%s: ignored explicit argument %q", name, value)
			}
			if supplied {
				args = append(args[:i+1], append([]string{"-" + value}, args[i+1:]...)...)
			}
			value = spec.name
		}
		if spec.name == "session" && o.Has("session-read-only") || spec.name == "session-read-only" && o.Has("session") {
			return o, fmt.Errorf("--session and --session-read-only are mutually exclusive")
		}
		dest := spec.dest
		if dest == "" {
			dest = spec.name
		}
		if spec.kind != "count" && spec.kind != "append" && dest != "format-options" {
			delete(o.values, dest)
		}
		if dest == "print" {
			switch spec.name {
			case "headers":
				value = "h"
			case "body":
				value = "b"
			case "meta":
				value = "m"
			}
		}
		if dest == "format-options" {
			switch spec.name {
			case "sorted", "no-unsorted":
				value = "headers.sort:true,json.sort_keys:true"
			case "unsorted", "no-sorted":
				value = "headers.sort:false,json.sort_keys:false"
			}
		}
		o.values[dest] = append(o.values[dest], value)
		if spec.name == "help" || spec.name == "manual" || spec.name == "version" {
			return o, nil
		}
	}
	for _, dest := range resets {
		delete(o.values, dest)
	}
	if err := o.validate(); err != nil {
		return o, err
	}
	if o.Has("offline") {
		delete(o.values, "download")
		delete(o.values, "continue")
	}
	return o, nil
}

func (o Options) validate() error {
	for _, name := range []string{"print", "history-print"} {
		for _, c := range o.Value(name) {
			if !strings.ContainsRune("HBhbm", c) {
				return fmt.Errorf("invalid output option: %c", c)
			}
		}
	}
	if v := o.Value("pretty"); v != "" && v != "all" && v != "colors" && v != "format" && v != "none" {
		return fmt.Errorf("invalid --pretty value: %s", v)
	}
	if v := o.Value("auth-type"); v != "" && v != "?" && v != "basic" && v != "digest" && v != "bearer" {
		return fmt.Errorf("invalid --auth-type: %s", v)
	}
	if o.Has("session") && o.Has("session-read-only") {
		return fmt.Errorf("--session and --session-read-only are mutually exclusive")
	}
	if o.Has("continue") && (!o.Has("download") || !o.Has("output")) {
		return fmt.Errorf("--continue requires --download and --output")
	}
	if o.Has("compress") && (o.Has("chunked") || o.Value("request-type") == "multipart") {
		return fmt.Errorf("cannot combine --compress and --chunked/--multipart")
	}
	return nil
}

func validateOption(name, value string) error {
	var choices []string
	switch name {
	case "pretty":
		choices = []string{"all", "colors", "format", "none"}
	case "style":
		choices = availableStyles()
	case "auth-type":
		choices = []string{"basic", "bearer", "digest"}
	case "ssl":
		choices = []string{"ssl2.3", "tls1", "tls1.1", "tls1.2", "tls1.3"}
	case "session", "session-read-only":
		_, err := sessionPath("", value)
		return err
	case "response-charset":
		if textEncoding(value) == nil {
			return fmt.Errorf("unknown encoding: %s", value)
		}
	case "response-mime":
		if strings.Count(value, "/") != 1 {
			return fmt.Errorf("invalid MIME type: %s", value)
		}
	case "cert", "cert-key":
		f, err := os.Open(value)
		if err != nil {
			return err
		}
		return f.Close()
	case "proxy":
		if !strings.Contains(value, ":") {
			return fmt.Errorf("invalid proxy specification: %s", value)
		}
	}
	if choices != nil && !slices.Contains(choices, value) {
		return fmt.Errorf("invalid --%s: %s", name, value)
	}
	return nil
}

func NewerVersion(candidate, current string) bool {
	parse := func(value string) []int {
		var version []int
		parts := strings.Split(value, ".")
		for _, part := range parts[:min(3, len(parts))] {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				break
			}
			version = append(version, n)
		}
		return version
	}
	a, b := parse(candidate), parse(current)
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return len(a) > len(b)
}
