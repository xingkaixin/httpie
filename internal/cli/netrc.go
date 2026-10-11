package cli

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type netrcLexer struct {
	text string
	pos  int
	line int
}

func (l *netrcLexer) token() string {
	for l.pos < len(l.text) && strings.ContainsRune("\n\t\r ", rune(l.text[l.pos])) {
		if l.text[l.pos] == '\n' {
			l.line++
		}
		l.pos++
	}
	quoted := l.pos < len(l.text) && l.text[l.pos] == '"'
	if quoted {
		l.pos++
	}
	var token strings.Builder
	for l.pos < len(l.text) {
		c := l.text[l.pos]
		l.pos++
		if c == '\n' {
			l.line++
		}
		if quoted && c == '"' || !quoted && strings.ContainsRune("\n\t\r ", rune(c)) {
			break
		}
		if c == '\\' && l.pos < len(l.text) {
			c = l.text[l.pos]
			l.pos++
			if c == '\n' {
				l.line++
			}
		}
		token.WriteByte(c)
	}
	return token.String()
}
func (l *netrcLexer) skipLine() string {
	start := l.pos
	if end := strings.IndexByte(l.text[start:], '\n'); end >= 0 {
		l.pos += end + 1
		l.line++
	} else {
		l.pos = len(l.text)
	}
	return l.text[start:l.pos]
}
func netrcAuth(host string) (string, string, bool) {
	filename := os.Getenv("NETRC")
	if filename == "" {
		home, _ := os.UserHomeDir()
		filename = filepath.Join(home, ".netrc")
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			filename = filepath.Join(home, "_netrc")
		}
	}
	data, err := os.ReadFile(filename)
	if err != nil || !utf8.Valid(data) {
		return "", "", false
	}
	l := netrcLexer{text: strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")}
	entries := map[string][2]string{}
	token := l.token()
	for token != "" {
		if strings.HasPrefix(token, "#") {
			if token == "#" && l.pos > 0 && l.text[l.pos-1] != '\n' {
				l.skipLine()
			}
			token = l.token()
			continue
		}
		if token == "macdef" {
			l.token()
			for {
				line := l.skipLine()
				if line == "" {
					return "", "", false
				}
				if line == "\n" {
					break
				}
			}
			token = l.token()
			continue
		}
		name := "default"
		if token == "machine" {
			name = l.token()
		} else if token != "default" {
			return "", "", false
		}
		if name == "" {
			return "", "", false
		}
		user, account, password := "", "", ""
		for {
			line := l.line
			token = l.token()
			if strings.HasPrefix(token, "#") {
				if l.line == line {
					l.skipLine()
				}
				continue
			}
			if token == "" || token == "machine" || token == "default" || token == "macdef" {
				break
			}
			switch token {
			case "login", "user":
				user = l.token()
			case "account":
				account = l.token()
			case "password":
				password = l.token()
			default:
				return "", "", false
			}
		}
		if user == "" {
			user = account
		}
		entries[name] = [2]string{user, password}
	}
	value, ok := entries[host]
	if !ok {
		value, ok = entries["default"]
	}
	return value[0], value[1], ok
}
