package main

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

var errQuote = errors.New("引号没有闭合")

// curlShort are the long names of the short options parseCurl knows.
var curlShort = map[byte]string{
	'X': "request", 'H': "header", 'b': "cookie", 'd': "data", 'F': "form", 'u': "user",
	'm': "max-time", 'A': "user-agent", 'e': "referer", 'k': "insecure", 'L': "location", 'I': "head",
}

var curlValued = map[string]bool{
	"url": true, "request": true, "header": true, "cookie": true, "user-agent": true, "referer": true,
	"data": true, "data-ascii": true, "data-binary": true, "data-raw": true, "json": true,
	"form": true, "user": true, "max-time": true,
}

// parseCurl returns the request of a curl command line, as browsers,
// Postman and API docs copy them. Options it does not know are taken for
// ones without a value.
func parseCurl(cmd string) (*request, error) {
	words, err := shellWords(cmd)
	if err != nil {
		return nil, err
	}
	r := newRequest("", "", "")
	r.Follow = false
	r.AuthType = authNone
	header := func(k, v string) { r.Headers = append(r.Headers, kvtable.Pair{On: true, Key: k, Value: v}) }
	var method, user, data, file string
	var sent, asJSON, head, digest bool
	apply := func(name, value string) {
		switch name {
		case "url":
			r.URL = value
		case "request":
			method = strings.ToUpper(value)
		case "header":
			k, v, ok := strings.Cut(value, ":")
			if !ok {
				// "Name;" sends Name empty, while "Name:" leaves Name out.
				if k, ok = strings.CutSuffix(k, ";"); ok {
					header(strings.TrimSpace(k), "")
				}
			} else if v = strings.TrimSpace(v); v != "" {
				header(strings.TrimSpace(k), v)
			}
		case "cookie":
			// Without "=", it names a file of cookies.
			if strings.Contains(value, "=") {
				header("Cookie", value)
			}
		case "user-agent":
			header("User-Agent", value)
		case "referer":
			header("Referer", value)
		case "data", "data-ascii", "data-binary", "data-raw", "json":
			switch {
			case name != "data-raw" && strings.HasPrefix(value, "@"):
				file = value[1:]
			case sent && name != "json":
				data += "&" + value
			default:
				data += value
			}
			sent, asJSON = true, asJSON || name == "json"
		case "form":
			k, v, _ := strings.Cut(value, "=")
			kind := kindText
			if strings.HasPrefix(v, "@") {
				kind, v = kindFile, v[1:]
			}
			r.Form = append(r.Form, kvtable.Pair{On: true, Key: k, Value: formValue(v, kind == kindFile), Kind: kind})
		case "user":
			user = value
		case "digest":
			digest = true
		case "insecure":
			r.Insecure = true
		case "location":
			r.Follow = true
		case "max-time":
			r.Timeout, _ = strconv.ParseFloat(value, 64)
		case "head":
			head = true
		}
	}
	for i := 1; i < len(words); i++ {
		word := words[i]
		if len(word) < 2 || word[0] != '-' {
			if r.URL == "" {
				r.URL = word
			}
			continue
		}
		names, value := []string{strings.TrimPrefix(word, "--")}, ""
		if !strings.HasPrefix(word, "--") {
			// Short options go together, as -sSL, the last with its value
			// after it or in the next word, as -XPOST or -X POST.
			names = nil
			for j := 1; j < len(word); j++ {
				names = append(names, curlShort[word[j]])
				if curlValued[curlShort[word[j]]] {
					value = word[j+1:]
					break
				}
			}
		}
		if curlValued[names[len(names)-1]] && value == "" && i+1 < len(words) {
			i++
			value = words[i]
		}
		for _, name := range names {
			apply(name, value)
		}
	}
	if r.URL == "" {
		return nil, errors.New("没有 URL")
	}
	r.Params = parse(r.URL, nil)

	if asJSON {
		for _, h := range [][2]string{{"Content-Type", "application/json"}, {"Accept", "application/json"}} {
			if headerIndex(r.Headers, h[0]) < 0 {
				header(h[0], h[1])
			}
		}
	}
	contentType := ""
	if i := headerIndex(r.Headers, "Content-Type"); i >= 0 {
		contentType = r.Headers[i].Value
	} else if sent {
		// curl sends data as a form unless told otherwise, and a Text body
		// would go as text/plain.
		header("Content-Type", "application/x-www-form-urlencoded")
	}
	switch {
	case len(r.Form) > 0:
		r.BodyType = bodyMultipart
	case file != "":
		r.BodyType, r.BodyFile = bodyFile, file
	case sent && strings.Contains(strings.ToLower(contentType), "json"):
		r.BodyType, r.Body = bodyJSON, data
	case sent:
		r.BodyType, r.Body = bodyText, data
	}

	switch {
	case method != "":
		r.Method = method
	case head:
		r.Method = "HEAD"
	case sent || len(r.Form) > 0:
		r.Method = "POST"
	default:
		r.Method = "GET"
	}
	if user != "" {
		r.AuthType = authBasic
		if digest {
			r.AuthType = authDigest
		}
		r.User, r.Password, _ = strings.Cut(user, ":")
	}
	return r, nil
}

func headerIndex(pairs []kvtable.Pair, name string) int {
	return slices.IndexFunc(pairs, func(p kvtable.Pair) bool { return strings.EqualFold(p.Key, name) })
}

// formValue returns the value of a -F field, which may be quoted, and of
// a file its path, without the options, as ;type=, after it.
func formValue(v string, file bool) string {
	if v, ok := strings.CutPrefix(v, `"`); ok {
		v, _, _ = strings.Cut(v, `"`)
		return v
	}
	if file {
		v, _, _ = strings.Cut(v, ";")
	}
	return v
}

// shellWords splits a command line into its words as a POSIX shell does,
// for the quoting copied commands use: '…', "…", bash's $'…', backslashes,
// and lines continued with one. It expands nothing.
func shellWords(s string) ([]string, error) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var words []string
	var w strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, w.String())
				w.Reset()
				inWord = false
			}
		case c == '\\':
			if i++; i < len(s) && s[i] != '\n' {
				w.WriteByte(s[i])
				inWord = true
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errQuote
			}
			w.WriteString(s[i+1 : i+1+end])
			i += 1 + end
			inWord = true
		case c == '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
					if i++; s[i] == '\n' {
						continue
					}
				}
				w.WriteByte(s[i])
			}
			if i == len(s) {
				return nil, errQuote
			}
			inWord = true
		case c == '$' && strings.HasPrefix(s[i+1:], "'"):
			end, err := ansiC(&w, s[i+2:])
			if err != nil {
				return nil, err
			}
			i += 2 + end
			inWord = true
		default:
			w.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, w.String())
	}
	return words, nil
}

// ansiC writes the text of bash's $'…' quoting, from s after its "$'", to
// w, and returns the index of the quote closing it in s. It knows the
// escapes browsers write: \n, \r, \t, \\, \', \", \xHH and \uHHHH.
func ansiC(w *strings.Builder, s string) (int, error) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			return i, nil
		case '\\':
			if i++; i == len(s) {
				return 0, errQuote
			}
			switch c := s[i]; c {
			case 'n':
				w.WriteByte('\n')
			case 'r':
				w.WriteByte('\r')
			case 't':
				w.WriteByte('\t')
			case '\\', '\'', '"':
				w.WriteByte(c)
			case 'x', 'u':
				digits := 2
				if c == 'u' {
					digits = 4
				}
				end := i + 1
				for end < len(s) && end <= i+digits && strings.IndexByte("0123456789abcdefABCDEF", s[end]) >= 0 {
					end++
				}
				n, err := strconv.ParseUint(s[i+1:end], 16, 32)
				switch {
				case err != nil:
					w.WriteString(`\` + string(c))
				case c == 'x':
					w.WriteByte(byte(n))
				default:
					w.WriteRune(rune(n))
				}
				i = end - 1
			default:
				w.WriteString(`\` + string(c))
			}
		default:
			w.WriteByte(s[i])
		}
	}
	return 0, errQuote
}
