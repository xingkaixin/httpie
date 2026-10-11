package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

type field struct {
	key   string
	value any
}
type object []field

func decodeJSON(data []byte) (any, error) {
	data, constants := jsonConstants(data)
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := jsonValue(d, constants)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after JSON value")
	}
	return v, nil
}
func jsonValue(d *json.Decoder, constants map[int64]json.Number) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		o := object{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			v, e := jsonValue(d, constants)
			if e != nil {
				return nil, e
			}
			o = append(o, field{k.(string), v})
		}
		_, err = d.Token()
		return o, err
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, e := jsonValue(d, constants)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		_, err = d.Token()
		return a, err
	default:
		if value, ok := constants[d.InputOffset()]; ok {
			return value, nil
		}
		if n, ok := t.(json.Number); ok {
			return pythonNumber(n), nil
		}
		return t, nil
	}
}
func jsonText(v any, indent int, sorted, ascii bool) []byte {
	var b bytes.Buffer
	var write func(any, int)
	quote := func(s string) string {
		var out bytes.Buffer
		e := json.NewEncoder(&out)
		e.SetEscapeHTML(false)
		_ = e.Encode(s)
		text := strings.TrimSuffix(out.String(), "\n")
		if !ascii {
			return text
		}
		var q strings.Builder
		for _, r := range text {
			if r > 127 {
				if r > 0xffff {
					a, z := utf16.EncodeRune(r)
					fmt.Fprintf(&q, "\\u%04x\\u%04x", a, z)
				} else {
					fmt.Fprintf(&q, "\\u%04x", r)
				}
			} else {
				q.WriteRune(r)
			}
		}
		return q.String()
	}
	line := func(depth int) {
		if indent >= 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*depth))
		}
	}
	write = func(v any, depth int) {
		switch v := v.(type) {
		case object:
			fields := append(object(nil), v...)
			if sorted {
				sort.SliceStable(fields, func(i, j int) bool {
					if fields[i].key == fields[j].key {
						left, lok := fields[i].value.(json.Number)
						right, rok := fields[j].value.(json.Number)
						if lok && rok {
							a, _ := left.Float64()
							b, _ := right.Float64()
							return a < b
						}
						return string(jsonText(fields[i].value, -1, true, false)) < string(jsonText(fields[j].value, -1, true, false))
					}
					return fields[i].key < fields[j].key
				})
			}
			b.WriteByte('{')
			for i, f := range fields {
				if i > 0 {
					b.WriteByte(',')
					if indent < 0 {
						b.WriteByte(' ')
					}
				}
				line(depth + 1)
				b.WriteString(quote(f.key))
				b.WriteString(": ")
				write(f.value, depth+1)
			}
			if len(fields) > 0 {
				line(depth)
			}
			b.WriteByte('}')
		case []any:
			b.WriteByte('[')
			for i, x := range v {
				if i > 0 {
					b.WriteByte(',')
					if indent < 0 {
						b.WriteByte(' ')
					}
				}
				line(depth + 1)
				write(x, depth+1)
			}
			if len(v) > 0 {
				line(depth)
			}
			b.WriteByte(']')
		case string:
			b.WriteString(quote(v))
		case json.Number:
			b.WriteString(string(v))
		case nil:
			b.WriteString("null")
		default:
			raw, _ := json.Marshal(v)
			b.Write(raw)
		}
	}
	write(v, 0)
	return b.Bytes()
}

type pathPart struct {
	key   string
	index int
	array bool
}

func jsonPath(raw string) ([]pathPart, error) {
	var parts []pathPart
	for pos := 0; pos < len(raw) || len(parts) == 0; {
		bracket := len(parts) > 0 || (pos < len(raw) && raw[pos] == '[')
		if bracket {
			if pos >= len(raw) || raw[pos] != '[' {
				return nil, fmt.Errorf("expecting '[' in %q", raw)
			}
			pos++
		}
		var key strings.Builder
		escapedNumber := false
		for pos < len(raw) {
			c := raw[pos]
			if c == '\\' && pos+1 < len(raw) {
				next := raw[pos+1]
				if next == '[' || next == ']' || next == '\\' || (key.Len() == 0 && (next >= '0' && next <= '9' || next == '-')) {
					if key.Len() == 0 && (next >= '0' && next <= '9' || next == '-') {
						escapedNumber = true
					}
					key.WriteByte(next)
					pos += 2
					continue
				}
			}
			if c == '[' || c == ']' {
				break
			}
			key.WriteByte(c)
			pos++
		}
		if bracket {
			if pos >= len(raw) || raw[pos] != ']' {
				return nil, fmt.Errorf("expecting ']' in %q", raw)
			}
			pos++
		}
		p := pathPart{key: key.String(), index: -1}
		if escapedNumber {
			if _, err := strconv.Atoi(p.key); err != nil {
				p.key = "\\" + p.key
			}
		}
		if bracket && !escapedNumber {
			if p.key == "" {
				p.array = true
			} else if n, err := strconv.Atoi(p.key); err == nil {
				if n < 0 {
					return nil, fmt.Errorf("negative indexes are not supported")
				}
				p.array = true
				p.index = n
			}
		}
		parts = append(parts, p)
		if pos == len(raw) {
			break
		}
	}
	return parts, nil
}
func setJSON(node any, path []pathPart, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	p := path[0]
	if p.array {
		var a []any
		if node != nil {
			var ok bool
			a, ok = node.([]any)
			if !ok {
				return nil, fmt.Errorf("cannot use array access on a non-array value")
			}
		}
		i := p.index
		if i < 0 {
			i = len(a)
		}
		for len(a) <= i {
			a = append(a, nil)
		}
		v, err := setJSON(a[i], path[1:], value)
		if err != nil {
			return nil, err
		}
		a[i] = v
		return a, nil
	}
	var o object
	if node != nil {
		var ok bool
		o, ok = node.(object)
		if !ok {
			return nil, fmt.Errorf("cannot use key access on a non-object value")
		}
	}
	for i := range o {
		if o[i].key == p.key {
			v, err := setJSON(o[i].value, path[1:], value)
			if err != nil {
				return nil, err
			}
			o[i].value = v
			return o, nil
		}
	}
	v, err := setJSON(nil, path[1:], value)
	if err != nil {
		return nil, err
	}
	return append(o, field{p.key, v}), nil
}

func pythonNumber(number json.Number) json.Number {
	text := string(number)
	if !strings.ContainsAny(text, ".eE") {
		if text == "-0" {
			return "0"
		}
		return number
	}
	value, _ := strconv.ParseFloat(text, 64)
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	format := byte('f')
	if absolute := math.Abs(value); absolute != 0 && (absolute < 1e-4 || absolute >= 1e16) {
		format = 'e'
	}
	text = strconv.FormatFloat(value, format, -1, 64)
	if !strings.ContainsAny(text, ".e") {
		text += ".0"
	}
	return json.Number(text)
}

func jsonConstants(data []byte) ([]byte, map[int64]json.Number) {
	var constants map[int64]json.Number
	quoted := false
	for i := 0; i < len(data); i++ {
		if quoted {
			if data[i] == '\\' {
				i++
			} else if data[i] == '"' {
				quoted = false
			}
			continue
		}
		if data[i] == '"' {
			quoted = true
			continue
		}
		if data[i] != 'N' && data[i] != 'I' && data[i] != '-' {
			continue
		}
		for _, name := range []string{"NaN", "Infinity", "-Infinity"} {
			if !bytes.HasPrefix(data[i:], []byte(name)) {
				continue
			}
			if constants == nil {
				data = bytes.Clone(data)
				constants = make(map[int64]json.Number)
			}
			constants[int64(i+1)] = json.Number(name)
			data[i] = '0'
			for j := 1; j < len(name); j++ {
				data[i+j] = ' '
			}
			i += len(name) - 1
			break
		}
	}
	return data, constants
}
