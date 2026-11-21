package main

import (
	"encoding/json"
	"slices"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
	"github.com/xingkaixin/mygo-native-ui/ui/templatefield"
)

type tokenKind int

const (
	tokKey tokenKind = iota
	tokString
	tokNumber
	tokLiteral
	tokNull
)

// token spans runes, as TextRanges counts them.
type token struct {
	start, end int
	kind       tokenKind
}

// jsonTokens finds the strings, numbers and literals of JSON. It tolerates
// broken JSON, as a body being typed is.
func jsonTokens(s string) []token {
	rs := []rune(s)
	var out []token
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(rs) && rs[j] != '"' && rs[j] != '\n' {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(rs))
			kind := tokString
			k := j
			for k < len(rs) && (rs[k] == ' ' || rs[k] == '\t') {
				k++
			}
			if k < len(rs) && rs[k] == ':' {
				kind = tokKey
			}
			out = append(out, token{i, j, kind})
			i = j
		case c == '-' || unicode.IsDigit(c):
			j := i + 1
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.' || rs[j] == 'e' || rs[j] == 'E' || rs[j] == '+' || rs[j] == '-') {
				j++
			}
			out = append(out, token{i, j, tokNumber})
			i = j
		case unicode.IsLetter(c):
			j := i + 1
			for j < len(rs) && unicode.IsLetter(rs[j]) {
				j++
			}
			switch string(rs[i:j]) {
			case "true", "false":
				out = append(out, token{i, j, tokLiteral})
			case "null":
				out = append(out, token{i, j, tokNull})
			}
			i = j
		default:
			i++
		}
	}
	return out
}

type palette [5]ui.Color

func jsonPalette(t *ui.Theme) palette {
	if t.Dark {
		return palette{ui.Hex("#c678dd"), ui.Hex("#98c379"), ui.Hex("#d19a66"), ui.Hex("#d19a66"), t.TextMuted}
	}
	return palette{ui.Hex("#a626a4"), ui.Hex("#3f8f3e"), ui.Hex("#b76b01"), ui.Hex("#b76b01"), t.TextMuted}
}

func colorRanges(tokens []token, p palette) []ui.TextRange {
	out := make([]ui.TextRange, len(tokens))
	for i, tk := range tokens {
		out[i] = ui.TextRange{Start: tk.start, End: tk.end, Color: p[tk.kind]}
	}
	return out
}

// highlighter caches the ranges and validation of one body text, which a
// frame asks for again and again while nothing changes.
type highlighter struct {
	text        string
	dark        bool
	cached      []ui.TextRange
	validInit   bool
	validText   string
	rawValid    bool
	scopeInit   bool
	scopeText   string
	scopeDark   bool
	envVars     []kvtable.Pair
	colVars     []kvtable.Pair
	filledValid bool
	varRanges   []ui.TextRange
}

func (h *highlighter) ranges(t *ui.Theme, text string) []ui.TextRange {
	if text != h.text || t.Dark != h.dark || h.cached == nil {
		h.text, h.dark = text, t.Dark
		h.cached = colorRanges(jsonTokens(text), jsonPalette(t))
	}
	return h.cached
}

func (h *highlighter) syncScope(t *ui.Theme, text string, sc scope) {
	if h.scopeInit && text == h.scopeText && t.Dark == h.scopeDark && slices.Equal(h.envVars, sc.envVars()) && slices.Equal(h.colVars, sc.colVars()) {
		return
	}
	h.scopeInit = true
	h.scopeText, h.scopeDark = text, t.Dark
	h.envVars, h.colVars = slices.Clone(sc.envVars()), slices.Clone(sc.colVars())
	h.varRanges = templatefield.Ranges(text, sc, t)
	if !h.validInit || text != h.validText {
		h.validInit = true
		h.validText = text
		h.rawValid = json.Valid([]byte(text))
	}
	h.filledValid = h.rawValid || text == "" || json.Valid([]byte(sc.fill(text)))
}

func (h *highlighter) jsonValid(t *ui.Theme, text string, sc scope) (raw, asSent bool) {
	h.syncScope(t, text, sc)
	return h.rawValid, h.filledValid
}

func (h *highlighter) templateRanges(t *ui.Theme, text string, sc scope) []ui.TextRange {
	h.syncScope(t, text, sc)
	return h.varRanges
}
