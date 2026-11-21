package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

// extract returns the variables rules take from body, a JSON document: a
// rule's key names a variable, and its value the path of the value, as
// jsonview's Copy Path writes it: $.data.token, $.items[0].id, $["a b"].
// A string is taken as its text, anything else as compact JSON. missing
// lists the paths body does not have.
func extract(body string, rules []kvtable.Pair) (vars []kvtable.Pair, missing []string) {
	for _, r := range rules {
		v, ok := lookup(json.RawMessage(body), r.Value)
		var b bytes.Buffer
		if !ok || json.Compact(&b, v) != nil {
			missing = append(missing, r.Value)
			continue
		}
		text := b.String()
		if strings.HasPrefix(text, `"`) {
			json.Unmarshal(b.Bytes(), &text)
		}
		vars = append(vars, kvtable.Pair{On: true, Key: r.Key, Value: text})
	}
	return vars, missing
}

// lookup returns the value at path in doc. It decodes only the objects
// and arrays on the way, keeping the value as the server wrote it.
func lookup(doc json.RawMessage, path string) (json.RawMessage, bool) {
	rest, ok := strings.CutPrefix(path, "$")
	if !ok {
		return nil, false
	}
	v := doc
	for rest != "" {
		var key string
		switch {
		case rest[0] == '.':
			end := strings.IndexAny(rest[1:], ".[") + 1
			if end == 0 {
				end = len(rest)
			}
			key, rest = rest[1:end], rest[end:]
		case strings.HasPrefix(rest, `["`):
			// The name is a JSON string, which may hold "]".
			d := json.NewDecoder(strings.NewReader(rest[1:]))
			if d.Decode(&key) != nil {
				return nil, false
			}
			end := 1 + int(d.InputOffset())
			if !strings.HasPrefix(rest[end:], "]") {
				return nil, false
			}
			rest = rest[end+1:]
		case rest[0] == '[':
			end := strings.IndexByte(rest, ']')
			var items []json.RawMessage
			if end < 0 || json.Unmarshal(v, &items) != nil {
				return nil, false
			}
			i, err := strconv.Atoi(rest[1:end])
			if err != nil || i < 0 || i >= len(items) {
				return nil, false
			}
			v, rest = items[i], rest[end+1:]
			continue
		default:
			return nil, false
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(v, &fields) != nil {
			return nil, false
		}
		if v, ok = fields[key]; !ok {
			return nil, false
		}
	}
	return v, true
}

func varNameForPath(path string) string {
	rest, ok := strings.CutPrefix(path, "$")
	if !ok {
		return "value"
	}
	var lastKey string
	var trailing []string
	for rest != "" {
		switch {
		case rest[0] == '.':
			end := strings.IndexAny(rest[1:], ".[") + 1
			if end == 0 {
				end = len(rest)
			}
			lastKey, trailing, rest = rest[1:end], nil, rest[end:]
		case strings.HasPrefix(rest, `["`):
			var key string
			d := json.NewDecoder(strings.NewReader(rest[1:]))
			if d.Decode(&key) != nil {
				return "value"
			}
			end := 1 + int(d.InputOffset())
			if !strings.HasPrefix(rest[end:], "]") {
				return "value"
			}
			lastKey, trailing, rest = key, nil, rest[end+1:]
		case rest[0] == '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return "value"
			}
			trailing = append(trailing, rest[1:end])
			rest = rest[end+1:]
		default:
			return "value"
		}
	}
	base := sanitizeVarName(lastKey)
	if base == "" {
		if len(trailing) == 0 {
			return "value"
		}
		base = "item"
	}
	if len(trailing) > 0 {
		base += "_" + strings.Join(trailing, "_")
	}
	return base
}

func sanitizeVarName(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
		if ok {
			b.WriteRune(r)
			prevUnderscore = r == '_'
		} else if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_.-")
}

func ensureExtractRule(r *request, path string) string {
	for _, e := range r.Extract {
		if e.On && strings.TrimSpace(e.Value) == path && e.Key != "" {
			return e.Key
		}
	}
	base := varNameForPath(path)
	name := base
	for n := 2; slices.ContainsFunc(r.Extract, func(e kvtable.Pair) bool { return e.Key == name }); n++ {
		name = base + "_" + strconv.Itoa(n)
	}
	r.Extract = append(r.Extract, kvtable.Pair{On: true, Key: name, Value: path})
	return name
}
