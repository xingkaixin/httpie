package main

import (
	"errors"
	"mime"
	"path/filepath"
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
	"data": true, "data-ascii": true, "data-binary": true, "data-raw": true, "data-urlencode": true, "json": true,
	"form": true, "form-string": true, "user": true, "max-time": true,
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
	var urlencoded []kvtable.Pair
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
		case "data-urlencode":
			k, v, _ := strings.Cut(value, "=")
			urlencoded = append(urlencoded, kvtable.Pair{On: true, Key: k, Value: v, Kind: kindText})
		case "form", "form-string":
			k, v, _ := strings.Cut(value, "=")
			kind := kindText
			if name == "form" && strings.HasPrefix(v, "@") {
				kind, v = kindFile, v[1:]
			}
			if name == "form" {
				v = formValue(v, kind == kindFile)
			}
			r.Form = append(r.Form, kvtable.Pair{On: true, Key: k, Value: v, Kind: kind})
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
	r.Params, r.PathVars = parse(r.URL, nil), pathVars(r.URL, nil)

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
	case len(urlencoded) > 0 && !sent:
		r.BodyType, r.Form = bodyForm, urlencoded
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
	case sent || len(r.Form) > 0 || len(urlencoded) > 0:
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

// curlCommand returns the curl command sending r, a request resolved, as
// n connects; a line for each option, as API docs show them.
func curlCommand(r *request, n *network) string {
	base, query, _ := split(completeRaw(r.URL))
	params := r.Params
	if r.AuthType == authAPIKey && r.In == inQuery && r.Key != "" {
		params = append(slices.Clone(params), kvtable.Pair{On: true, Key: r.Key, Value: r.Token})
	}
	if extra := strings.TrimPrefix(withQuery("", params), "?"); extra != "" {
		if query != "" {
			query += "&"
		}
		query += extra
	}
	if query != "" {
		base += "?" + query
	}
	first := []string{"curl"}
	var lines [][]string
	opt := func(words ...string) { lines = append(lines, words) }
	hasBody := r.BodyType != bodyNone && (r.Body != "" || len(enabled(r.Form)) > 0 || r.BodyFile != "")
	switch {
	case r.Method == "HEAD":
		first = append(first, "--head")
	case r.Method != "GET" || hasBody:
		first = append(first, "-X", r.Method)
	}
	first = append(first, base)
	headers := enabled(r.Headers)
	has := func(name string) bool { return headerIndex(headers, name) >= 0 }
	for _, h := range headers {
		if h.Value == "" {
			opt("-H", h.Key+";")
		} else {
			opt("-H", h.Key+": "+h.Value)
		}
	}
	switch r.AuthType {
	case authBasic:
		opt("-u", r.User+":"+r.Password)
	case authDigest:
		opt("--digest", "-u", r.User+":"+r.Password)
	case authBearer:
		opt("-H", "Authorization: Bearer "+r.Token)
	case authAPIKey:
		if r.Key != "" && r.In != inQuery {
			opt("-H", r.Key+": "+r.Token)
		}
	}
	if hasBody {
		switch r.BodyType {
		case bodyJSON:
			// As the http command sends JSON.
			if !has("Content-Type") {
				opt("-H", "Content-Type: application/json")
			}
			if !has("Accept") {
				opt("-H", "Accept: application/json, */*;q=0.5")
			}
			opt("--data-raw", r.Body)
		case bodyText:
			if !has("Content-Type") {
				opt("-H", "Content-Type: text/plain; charset=utf-8")
			}
			opt("--data-raw", r.Body)
		case bodyForm:
			for _, f := range enabled(r.Form) {
				opt("--data-urlencode", f.Key+"="+f.Value)
			}
		case bodyMultipart:
			for _, f := range enabled(r.Form) {
				if f.Kind == kindFile {
					opt("-F", f.Key+"=@"+f.Value)
				} else {
					opt("--form-string", f.Key+"="+f.Value)
				}
			}
		case bodyFile:
			if t := mime.TypeByExtension(filepath.Ext(r.BodyFile)); t != "" && !has("Content-Type") {
				opt("-H", "Content-Type: "+t)
			}
			opt("--data-binary", "@"+r.BodyFile)
		}
	}
	if r.Follow {
		opt("-L")
	}
	if r.Insecure {
		opt("-k")
	}
	if r.Timeout > 0 {
		opt("--max-time", strconv.FormatFloat(r.Timeout, 'f', -1, 64))
	}
	if n.ProxyMode == proxyCustom && n.ProxyURL != "" {
		opt("-x", n.ProxyURL)
	}
	if n.CA != "" {
		opt("--cacert", n.CA)
	}
	if c := n.cert(r.URL); c != nil {
		opt("--cert", c.Cert)
		if c.Key != "" {
			opt("--key", c.Key)
		}
	}
	var b strings.Builder
	b.WriteString(quoteWords(first))
	for _, l := range lines {
		b.WriteString(" \\\n  " + quoteWords(l))
	}
	return b.String()
}
