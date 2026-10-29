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
