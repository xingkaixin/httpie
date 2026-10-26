package cli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"httpie/internal/core"
)

//go:embed help.txt
var help string

//go:embed manager-help.json
var managerHelpData []byte

//go:embed args.json
var argumentSchema []byte

func runManager(args []string, stdout, stderr io.Writer) (code int, err error) {
	traceback := false
	for _, arg := range args {
		if arg == "--debug" {
			fmt.Fprintf(stderr, "HTTPie %s\nGo %s\nPlatform %s/%s\nConfig directory: %s\n", core.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, core.ConfigDir())
		}
		traceback = traceback || arg == "--debug" || arg == "--traceback"
	}
	if traceback {
		defer func() {
			if err != nil {
				_, _ = stderr.Write(debug.Stack())
			}
		}()
	}
	if len(args) == 1 && args[0] == "--debug" {
		return 0, nil
	}
	var words []string
	bind, stopped := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		command := strings.Join(words[:min(len(words), 3)], " ")
		if arg == "--" {
			stopped = true
			continue
		}
		if !stopped && strings.HasPrefix(arg, "-") {
			name, value, supplied := strings.Cut(arg, "=")
			flags := []string{"--help"}
			if len(words) == 0 {
				flags = append(flags, "--version", "--debug", "--traceback")
			}
			if command == "cli export-args" {
				flags = append(flags, "--format")
				if strings.HasPrefix(arg, "-f") {
					name, value = "--format", strings.TrimPrefix(arg[2:], "=")
					supplied = len(arg) > 2
				}
			}
			if command == "cli sessions upgrade" || command == "cli sessions upgrade-all" {
				flags = append(flags, "--bind-cookies")
			}
			if name == "-h" {
				name = "--help"
			} else if strings.HasPrefix(name, "--") {
				matched := ""
				for _, flag := range flags {
					if strings.HasPrefix(flag, name) {
						if matched != "" {
							return 1, fmt.Errorf("ambiguous option: %s", name)
						}
						matched = flag
					}
				}
				name = matched
			}
			if supplied && name != "" && name != "--format" {
				return 1, fmt.Errorf("argument %s does not take a value", name)
			}
			switch name {
			case "--help":
				var pages map[string]string
				_ = json.Unmarshal(managerHelpData, &pages)
				fmt.Fprint(stdout, pages[command])
				return 0, nil
			case "--version":
				fmt.Fprintln(stdout, core.Version)
				return 0, nil
			case "--bind-cookies":
				bind = true
			case "--format":
				if !supplied {
					if i+1 == len(args) || core.OptionLike(args[i+1]) {
						return 1, fmt.Errorf("--format requires a value")
					}
					i++
					value = args[i]
				}
				if value != "json" {
					return 1, fmt.Errorf("export format must be json")
				}
			}
			continue
		}
		valid := false
		switch command {
		case "":
			valid = arg == "cli"
		case "cli":
			valid = arg == "export-args" || arg == "check-updates" || arg == "sessions"
		case "cli sessions":
			valid = arg == "upgrade" || arg == "upgrade-all"
		case "cli sessions upgrade":
			if len(words) < 5 {
				words = append(words, arg)
			}
			continue
		default:
			continue
		}
		if !valid {
			return 1, fmt.Errorf("unknown management command: %s", arg)
		}
		words = append(words, arg)
	}
	if len(words) < 2 || words[0] != "cli" {
		return 1, fmt.Errorf("specify 'httpie cli export-args', 'check-updates', or 'sessions'")
	}
	switch words[1] {
	case "export-args":
		_, err := stdout.Write(argumentSchema)
		return 0, err
	case "check-updates":
		if err := fetchUpdates(); err != nil {
			return 1, err
		}
		_, err := io.WriteString(stdout, updateStatus())
		return 0, err
	case "sessions":
		if len(words) < 3 {
			return 1, fmt.Errorf("specify sessions upgrade or upgrade-all")
		}
		upgrade := func(host, name string) error {
			path, upgraded, err := core.UpgradeSession(host, name, bind)
			if err != nil {
				return err
			}
			label := strings.TrimSuffix(filepath.Base(path), ".json")
			if !upgraded {
				fmt.Fprintf(stdout, "'%s' @ '%s' is already up to date.\n", label, host)
				return nil
			}
			fmt.Fprintf(stdout, "Upgraded '%s' @ '%s' to v%s\n", label, host, core.Version)
			return nil
		}
		switch words[2] {
		case "upgrade":
			if len(words) != 5 {
				return 1, fmt.Errorf("upgrade requires HOSTNAME SESSION_NAME_OR_PATH")
			}
			if err := upgrade(words[3], words[4]); err != nil {
				return 1, err
			}
			return 0, nil
		case "upgrade-all":
			directory := filepath.Join(core.ConfigDir(), "sessions")
			if _, err := os.Stat(directory); err != nil {
				return 1, err
			}
			paths, err := filepath.Glob(filepath.Join(directory, "*", "*.json"))
			if err != nil {
				return 1, err
			}
			for _, path := range paths {
				host := filepath.Base(filepath.Dir(path))
				if err = upgrade(host, path); err != nil {
					return 1, err
				}
			}
			return 0, nil
		}
	}
	return 1, fmt.Errorf("unknown management command: %s", strings.Join(words, " "))
}
