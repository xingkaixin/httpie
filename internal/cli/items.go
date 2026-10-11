package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type item struct{ key, sep, value string }

func parseItem(raw string) (item, error) {
	var key, value strings.Builder
	part := &key
	separator := ""
	separators := []string{":=@", "==@", ":=", "=@", "==", ":@", ":", ";", "=", "@"}
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if !strings.ContainsRune(":;=@", rune(raw[i+1])) {
				part.WriteByte('\\')
			}
			i++
			part.WriteByte(raw[i])
			continue
		}
		if separator == "" {
			for _, sep := range separators {
				if strings.HasPrefix(raw[i:], sep) {
					separator = sep
					part = &value
					i += len(sep) - 1
					break
				}
			}
			if separator != "" {
				continue
			}
		}
		part.WriteByte(raw[i])
	}
	if separator == "" {
		return item{}, fmt.Errorf("invalid request item: %q", raw)
	}
	return item{key.String(), separator, value.String()}, nil
}
func groupItems(items []item) []item {
	var keys []string
	groups := map[string][]item{}
	for _, it := range items {
		if _, ok := groups[it.key]; !ok {
			keys = append(keys, it.key)
		}
		groups[it.key] = append(groups[it.key], it)
	}
	var result []item
	for _, key := range keys {
		result = append(result, groups[key]...)
	}
	return result
}
func expandPath(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	name, rest, _ := strings.Cut(path[1:], "/")
	home, _ := os.UserHomeDir()
	if name != "" {
		account, err := user.Lookup(name)
		if err != nil {
			return path
		}
		home = account.HomeDir
	}
	return filepath.Join(home, rest)
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
