package main

import (
	"net/url"
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
