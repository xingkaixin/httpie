package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type item struct{ key, sep, value string }

func parseItem(raw string) (item, error) {
	var key strings.Builder
	separators := []string{":=@", "==@", ":=", "=@", "==", ":@", ":", ";", "=", "@"}
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) && strings.ContainsRune(":;=@", rune(raw[i+1])) {
			i++
			key.WriteByte(raw[i])
			continue
		}
		for _, sep := range separators {
			if strings.HasPrefix(raw[i:], sep) {
				return item{key.String(), sep, raw[i+len(sep):]}, nil
			}
		}
		key.WriteByte(raw[i])
	}
	return item{}, fmt.Errorf("invalid request item: %q", raw)
}
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}
func embeddedText(path string) (string, error) {
	data, err := os.ReadFile(expandPath(path))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("cannot embed %q: not UTF-8 or ASCII text", path)
	}
	return string(data), nil
}
