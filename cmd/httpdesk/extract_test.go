package main

import (
	"slices"
	"testing"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

func TestExtract(t *testing.T) {
	body := `{
  "data": {"token": "a.b-c", "id": 9007199254740993, "ok": true, "none": null},
  "items": [{"id": "x"}, {"tags": ["t1"]}],
  "a b": {"]\"": 1}
}`
	rule := func(path string) kvtable.Pair { return kvtable.Pair{On: true, Key: path, Value: path} }
	rules := []kvtable.Pair{
		rule("$.data.token"), rule("$.data.id"), rule("$.data.ok"), rule("$.data.none"),
		rule("$.items[0].id"), rule("$.items[1]"), rule(`$["a b"]["]\""]`),
		rule("$.data.missing"), rule("$.items[2]"), rule("$.data.token.x"), rule("data.token"),
	}
	vars, missing := extract(body, rules)
	want := []string{"a.b-c", "9007199254740993", "true", "null", "x", `{"tags":["t1"]}`, "1"}
	var got []string
	for _, v := range vars {
		got = append(got, v.Value)
	}
	if !slices.Equal(got, want) {
		t.Errorf("values %q, want %q", got, want)
	}
	if want := []string{"$.data.missing", "$.items[2]", "$.data.token.x", "data.token"}; !slices.Equal(missing, want) {
		t.Errorf("missing %q, want %q", missing, want)
	}
}

func TestVarNameForPathAndEnsureExtractRule(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"$.data.token", "token"},
		{"$.items[0].id", "id"},
		{"$.items[0]", "items_0"},
		{"$[0]", "item_0"},
		{`$["a b"]`, "a_b"},
		{`$["$id"]`, "id"},
		{"$", "value"},
	} {
		if got := varNameForPath(tc.path); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.path, got, tc.want)
		}
	}
	r := newRequest("t", "GET", "example.org")
	if got := ensureExtractRule(r, "$.a.id"); got != "id" {
		t.Fatalf("first: %q", got)
	}
	if got := ensureExtractRule(r, "$.a.id"); got != "id" || len(r.Extract) != 1 {
		t.Fatalf("repeat: %q len=%d", got, len(r.Extract))
	}
	if got := ensureExtractRule(r, "$.b.id"); got != "id_2" || len(r.Extract) != 2 {
		t.Fatalf("collision: %q len=%d", got, len(r.Extract))
	}
}
