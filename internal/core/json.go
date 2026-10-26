package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
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

func sortJSON(value any) error {
	var failure error
	switch value := value.(type) {
	case object:
		sort.SliceStable(value, func(i, j int) bool {
			if value[i].key != value[j].key {
				return value[i].key < value[j].key
			}
			order, err := compareJSON(value[i].value, value[j].value)
			if err != nil {
				failure = err
			}
			return order < 0
		})
		if failure != nil {
			return failure
		}
		for _, field := range value {
			if err := sortJSON(field.value); err != nil {
				return err
			}
		}
	case []any:
		for _, element := range value {
			if err := sortJSON(element); err != nil {
				return err
			}
		}
	}
	return nil
}

func compareJSON(a, b any) (int, error) {
	numeric := func(value any) (json.Number, bool) {
		if value, ok := value.(bool); ok {
			if value {
				return "1", true
			}
			return "0", true
		}
		number, ok := value.(json.Number)
		return number, ok
	}
	if a, ok := numeric(a); ok {
		if b, ok := numeric(b); ok {
			rational := func(n json.Number) *big.Rat {
				if !strings.ContainsAny(string(n), ".eENaI") {
					value, _ := new(big.Rat).SetString(string(n))
					return value
				}
				value, _ := n.Float64()
				return new(big.Rat).SetFloat64(value)
			}
			x, y := rational(a), rational(b)
			if x != nil && y != nil {
				return x.Cmp(y), nil
			}
			xf, _ := a.Float64()
			yf, _ := b.Float64()
			if xf < yf {
				return -1, nil
			}
			if xf > yf {
				return 1, nil
			}
			return 0, nil
		}
	}
	switch a := a.(type) {
	case nil:
		if b == nil {
			return 0, nil
		}
	case string:
		if b, ok := b.(string); ok {
			return strings.Compare(a, b), nil
		}
	case object:
		// Upstream's duplicate-key dict stores the same sentinel for every nonempty object.
		if b, ok := b.(object); ok && (len(a) == 0) == (len(b) == 0) {
			return 0, nil
		}
	case []any:
		if b, ok := b.([]any); ok {
			for i := 0; i < min(len(a), len(b)); i++ {
				order, err := compareJSON(a[i], b[i])
				if err != nil || order != 0 {
					return order, err
				}
			}
			return len(a) - len(b), nil
		}
	}
	return 0, fmt.Errorf("cannot compare JSON values %T and %T while sorting duplicate keys", a, b)
}

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
func jsonText(v any, indent int, ascii bool) []byte {
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
			fields := v
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
