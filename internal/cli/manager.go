package cli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

//go:embed manual.txt
var manual string

//go:embed help.txt
var help string

//go:embed manager-help.json
var managerHelpData []byte

//go:embed args.json
var argumentSchema []byte

func runManager(args []string, stdout, stderr io.Writer) (int, error) {
	var words []string
	bind := false
	for _, arg := range args {
		switch arg {
		case "--version":
			fmt.Fprintln(stdout, version)
			return 0, nil
		case "--help", "-h":
			var pages map[string]string
			_ = json.Unmarshal(managerHelpData, &pages)
			text := pages[strings.Join(words, " ")]
			if text == "" {
				text = pages[""]
			}
			fmt.Fprint(stdout, text)
			return 0, nil
		case "--debug", "--traceback":
		case "--bind-cookies":
			bind = true
		default:
			words = append(words, arg)
		}
	}
	if len(words) < 2 || words[0] != "cli" {
		return 1, fmt.Errorf("specify 'httpie cli export-args', 'check-updates', or 'sessions'")
	}
	switch words[1] {
	case "export-args":
		if len(words) > 2 && !(len(words) == 4 && (words[2] == "-f" || words[2] == "--format") && words[3] == "json") && !(len(words) == 3 && words[2] == "--format=json") {
			return 1, fmt.Errorf("export format must be json")
		}
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
			s, err := loadSession(host, name)
			if err != nil {
				return err
			}
			if !s.exists {
				return fmt.Errorf("session does not exist: %s", s.path)
			}
			meta := map[string]any{}
			_ = json.Unmarshal(s.original["__meta__"], &meta)
			current, ok := meta["httpie"].(string)
			if !ok {
				current = version
			}
			label := strings.TrimSuffix(filepath.Base(s.path), ".json")
			if !newerVersion("3.2.0", current) {
				fmt.Fprintf(stdout, "'%s' @ '%s' is already up to date.\n", label, host)
				return nil
			}
			if newerVersion("3.1.0", current) {
				for i := range s.cookies {
					c := &s.cookies[i]
					if c.Domain == nil || *c.Domain == "" {
						if bind {
							domain := strings.Split(host, ":")[0]
							c.Domain = &domain
						} else {
							c.Domain = nil
						}
					}
				}
				s.original["cookies"] = json.RawMessage("[]")
			}
			s.original["headers"] = json.RawMessage("[]")
			meta["httpie"] = version
			s.original["__meta__"], _ = json.Marshal(meta)
			if err = s.save(); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Upgraded '%s' @ '%s' to v%s\n", label, host, version)
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
			paths, err := filepath.Glob(filepath.Join(configDir(), "sessions", "*", "*.json"))
			if err != nil {
				return 1, err
			}
			for _, path := range paths {
				host := strings.ReplaceAll(filepath.Base(filepath.Dir(path)), "_", ":")
				if err = upgrade(host, path); err != nil {
					return 1, err
				}
			}
			return 0, nil
		}
	}
	return 1, fmt.Errorf("unknown management command: %s", strings.Join(words, " "))
}
