package main

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

// split returns the parts of a URL around its query: what comes before
// the "?", the query, and the fragment with its "#".
func split(raw string) (base, query, fragment string) {
	base = raw
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base, fragment = base[:i], base[i:]
	}
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base, query = base[:i], base[i+1:]
	}
	return base, query, fragment
}

// parse returns the parameters of raw's query, in order, followed by
// those of old that are off, which the query leaves out.
func parse(raw string, old []kvtable.Pair) []kvtable.Pair {
	_, query, _ := split(raw)
	var pairs []kvtable.Pair
	for part := range strings.SplitSeq(query, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		pairs = append(pairs, kvtable.Pair{Key: unescape(k), Value: unescape(v), On: true})
	}
	for _, p := range old {
		if !p.On {
			pairs = append(pairs, p)
		}
	}
	return pairs
}

// unescape decodes a part of a query, leaving it as typed when it is not
// escaped well, as while it is being typed.
func unescape(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// withQuery returns raw with the parameters on as its query.
func withQuery(raw string, pairs []kvtable.Pair) string {
	base, _, fragment := split(raw)
	var q strings.Builder
	for _, p := range pairs {
		if !p.On || p.Key == "" {
			continue
		}
		if q.Len() > 0 {
			q.WriteByte('&')
		}
		q.WriteString(escape(p.Key))
		if p.Value != "" {
			q.WriteByte('=')
			q.WriteString(escape(p.Value))
		}
	}
	if q.Len() == 0 {
		return base + fragment
	}
	return base + "?" + q.String() + fragment
}

// escape encodes a part of a query, keeping the braces of template
// variables such as {{token}} as typed.
func escape(s string) string {
	e := url.QueryEscape(s)
	return strings.NewReplacer("%7B", "{", "%7D", "}").Replace(e)
}

// pathVar is a variable of a URL's path, as :id in /users/:id.
var pathVar = regexp.MustCompile(`/:([A-Za-z_][\w-]*)`)

// pathSpan returns where the path of raw is: after its host, which may
// have a port, up to its query or fragment.
func pathSpan(raw string) (start, end int) {
	base, _, _ := split(raw)
	host := 0
	if i := strings.Index(base, "://"); i >= 0 {
		host = i + 3
	}
	i := strings.IndexByte(base[host:], '/')
	if i < 0 {
		return len(base), len(base)
	}
	return host + i, len(base)
}

// pathVars returns the variables of raw's path, with the values old has
// for them.
func pathVars(raw string, old []kvtable.Pair) []kvtable.Pair {
	start, end := pathSpan(raw)
	var vars []kvtable.Pair
	for _, m := range pathVar.FindAllStringSubmatch(raw[start:end], -1) {
		v := kvtable.Pair{On: true, Key: m[1]}
		if i := slices.IndexFunc(old, func(o kvtable.Pair) bool { return o.Key == v.Key }); i >= 0 {
			v.Value = old[i].Value
		}
		vars = append(vars, v)
	}
	return vars
}

// fillPath returns raw with the variables of its path that vars give a
// value replaced by it, escaped.
func fillPath(raw string, vars []kvtable.Pair) string {
	start, end := pathSpan(raw)
	path := pathVar.ReplaceAllStringFunc(raw[start:end], func(m string) string {
		i := slices.IndexFunc(vars, func(v kvtable.Pair) bool { return v.Key == m[2:] })
		if i < 0 || vars[i].Value == "" {
			return m
		}
		return "/" + url.PathEscape(vars[i].Value)
	})
	return raw[:start] + path + raw[end:]
}
