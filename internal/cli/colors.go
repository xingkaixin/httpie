package cli

import (
	_ "embed"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

//go:embed palettes.json
var paletteData []byte
var palettes map[string]map[string][2]string
var loadPalettes sync.Once

func availableStyles() []string {
	loadPalettes.Do(func() { _ = json.Unmarshal(paletteData, &palettes) })
	var names []string
	for name := range palettes {
		if !strings.HasSuffix(name, "-headers") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
func (o *output) color(text, media string) string {
	loadPalettes.Do(func() { _ = json.Unmarshal(paletteData, &palettes) })
	style := o.options.value("style")
	if style != "auto" && !strings.Contains(os.Getenv("TERM"), "256color") {
		style = "auto"
	}
	if media == "http" && palettes[style+"-headers"] != nil {
		style += "-headers"
	}
	palette := palettes[style]
	if palette == nil {
		return text
	}
	var lexer chroma.Lexer
	jsonBody := strings.Contains(media, "json")
	if media != "http" && !jsonBody {
		if media == "text/plain" {
			lexer = lexers.Get("text")
		} else if media != "application/octet-stream" {
			lexer = lexers.MatchMimeType(media)
		}
		if lexer == nil {
			_, subtype, _ := strings.Cut(media, "/")
			for _, name := range strings.Split(subtype, "+") {
				if candidate := lexers.Get(name); candidate != nil {
					lexer = candidate
				}
			}
		}
		if o.options.value("request-type") == "json" && (lexer == nil || media == "text/plain") && jsonCandidate(text) {
			jsonBody = true
		}
		if !jsonBody && lexer == nil {
			return text
		}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Trim(text, "\n") + "\n"
	var tokens []chroma.Token
	switch {
	case media == "http":
		tokens = httpTokens(text, style != "auto")
	case jsonBody:
		tokens = jsonTokens(text)
	case media == "text/plain":
		return text
	default:
		if strings.Contains(media, "xml") {
			lexer = xmlColorLexer
		}
		iterator, err := lexer.Tokenise(nil, text)
		if err != nil {
			return text
		}
		tokens = iterator.Tokens()
	}
	var b strings.Builder
	statusToken := ""
	for _, token := range tokens {
		kind := token.Type
		escape, ok := palette[kind.String()]
		if strings.HasSuffix(style, "-headers") {
			key := ""
			if kind == chroma.NameFunction {
				key = "NameFunctionHTTP" + token.Value
			}
			if number, err := strconv.Atoi(token.Value); kind == chroma.LiteralNumber && err == nil && number >= 100 && number < 600 {
				statusToken = map[byte]string{'1': "INFO", '2': "OK", '3': "REDIRECT", '4': "CLIENT_ERR", '5': "SERVER_ERR"}[token.Value[0]]
			}
			if kind == chroma.LiteralNumber && statusToken != "" {
				key = "LiteralNumberHTTP" + statusToken
			}
			if special, exists := palette[key]; exists {
				escape, ok = special, true
			}
		}
		for !ok && kind != 0 {
			kind = kind.Parent()
			escape, ok = palette[kind.String()]
		}
		parts := strings.Split(token.Value, "\n")
		for i, line := range parts {
			if i > 0 {
				b.WriteByte('\n')
			}
			if line != "" || style == "auto" && i < len(parts)-1 {
				b.WriteString(escape[0])
				b.WriteString(line)
				b.WriteString(escape[1])
			}
		}
	}
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}
func jsonCandidate(text string) bool { _, err := decodeJSON([]byte(text)); return err == nil }

var jsonPrefix = regexp.MustCompile(`(?is)^([^{\["]+)((?:[{\["]|true|false|null).+)`)

func jsonTokens(text string) []chroma.Token {
	var tokens []chroma.Token
	if match := jsonPrefix.FindStringSubmatch(text); match != nil {
		tokens = append(tokens, chroma.Token{Type: chroma.Error, Value: match[1]})
		text = match[2]
	}
	for i := 0; i < len(text); {
		start := i
		kind := chroma.Error
		switch c := text[i]; {
		case c == '"':
			i++
			for i < len(text) {
				if text[i] == '\\' {
					i += 2
					continue
				}
				if text[i] == '"' {
					i++
					break
				}
				i++
			}
			if i > len(text) {
				i = len(text)
			}
			kind = chroma.LiteralStringDouble
			next := i
			for next < len(text) && unicode.IsSpace(rune(text[next])) {
				next++
			}
			if next < len(text) && text[next] == ':' {
				kind = chroma.NameTag
			}
		case strings.ContainsRune("{}[],:", rune(c)):
			kind = chroma.Punctuation
			i++
			for i < len(text) && strings.ContainsRune("{}[],", rune(text[i])) {
				i++
			}
		case unicode.IsSpace(rune(c)):
			kind = chroma.TextWhitespace
			i++
			for i < len(text) && unicode.IsSpace(rune(text[i])) {
				i++
			}
		case c == '-' || c >= '0' && c <= '9':
			kind = chroma.LiteralNumberInteger
			i++
			for i < len(text) && strings.ContainsRune("0123456789eE.+-", rune(text[i])) {
				if strings.ContainsRune(".eE", rune(text[i])) {
					kind = chroma.LiteralNumberFloat
				}
				i++
			}
		case strings.HasPrefix(text[i:], "//"):
			kind = chroma.CommentSingle
			i += 2
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case strings.HasPrefix(text[i:], "/*"):
			if end := strings.Index(text[i+2:], "*/"); end >= 0 {
				kind = chroma.CommentMultiline
				i += end + 4
			} else {
				i = len(text)
			}
		case c == 'f' || c == 'n' || c == 't':
			kind = chroma.KeywordConstant
			i++
			for i < len(text) && strings.ContainsRune("truefalsenull", rune(text[i])) {
				i++
			}
		default:
			i++
		}
		tokens = append(tokens, chroma.Token{Type: kind, Value: text[start:i]})
	}
	return tokens
}
func httpTokens(text string, simplified bool) []chroma.Token {
	var tokens []chroma.Token
	add := func(kind chroma.TokenType, value string) {
		tokens = append(tokens, chroma.Token{Type: kind, Value: value})
	}
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			add(chroma.Text, "\n")
		}
		if i == 0 {
			words := strings.SplitN(line, " ", 3)
			if strings.HasPrefix(line, "HTTP/") && len(words) == 3 {
				add(chroma.KeywordReserved, "HTTP")
				add(chroma.Operator, "/")
				add(chroma.LiteralNumber, words[0][5:])
				add(chroma.Text, " ")
				add(chroma.LiteralNumber, words[1])
				add(chroma.Text, " ")
				kind := chroma.NameException
				if simplified {
					kind = chroma.LiteralNumber
				}
				add(kind, words[2])
			} else if len(words) == 3 {
				add(chroma.NameFunction, words[0])
				add(chroma.Text, " ")
				add(chroma.NameNamespace, words[1])
				add(chroma.Text, " ")
				add(chroma.KeywordReserved, "HTTP")
				add(chroma.Operator, "/")
				add(chroma.LiteralNumber, strings.TrimPrefix(words[2], "HTTP/"))
			} else {
				add(chroma.Text, line)
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			add(chroma.Text, line)
			continue
		}
		add(chroma.NameAttribute, name)
		add(chroma.Operator, ":")
		space := len(value) - len(strings.TrimLeft(value, " \t"))
		add(chroma.Text, value[:space])
		kind := chroma.Literal
		if simplified {
			kind = chroma.LiteralString
		}
		add(kind, value[space:])
	}
	return tokens
}
