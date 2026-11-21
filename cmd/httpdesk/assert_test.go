package main

import (
	"testing"
	"time"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"

	"httpie/internal/core"
)

func TestRunChecks(t *testing.T) {
	res := &response{
		Code: 201, Duration: 120 * time.Millisecond, JSON: true,
		Headers: []core.Header{{Name: "Content-Type", Value: "application/json"}},
		Body:    `{"data": {"id": 42, "name": "kev", "tags": ["a"], "none": null}}`,
	}
	pass := map[string]string{
		"status": "2xx", "time": "< 500", "header content-type": "contains json", "$.data.id": "> 40",
		"$.data.name": "kev", "$.data.tags": `== ["a"]`, "$.data.none": "== null", "$.data.missing": "!exists", "body": "matches \"id\":\\s*42",
	}
	fail := map[string]string{
		"status": "== 200", "time": "<= 100", "header X-Id": "exists", "$.data.id": "== 41", "$.data.name": "!= kev", "$.data.missing": "== 1", "$.data.tags": "> 1",
	}
	for want, rules := range map[bool]map[string]string{true: pass, false: fail} {
		for k, v := range rules {
			got := runChecks([]kvtable.Pair{{On: true, Key: k, Value: v}}, res)
			if got[0].Pass != want {
				t.Errorf("%s %s: pass %v, %q", k, v, got[0].Pass, got[0].Detail)
			}
		}
	}
}

func TestAddTestForPath(t *testing.T) {
	res := &response{
		Code: 200, JSON: true,
		Body: `{"data": {"id": 42, "name": "kev", "empty": "", "padded": " x ", "cls": "2xx", "none": null, "tags": ["a"]}}`,
	}
	r := newRequest("t", "GET", "example.org")
	cases := []struct{ path, want string }{
		{"$.data.id", "== 42"},
		{"$.data.name", "== kev"},
		{"$.data.empty", `== ""`},
		{"$.data.padded", `== " x "`},
		{"$.data.cls", `== "2xx"`},
		{"$.data.none", "== null"},
		{"$.data.tags", "exists"},
	}
	for _, tc := range cases {
		if got := addTestForPath(r, res, tc.path); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.path, got, tc.want)
		}
	}
	if len(r.Tests) != len(cases) || len(res.Checks) != len(cases) {
		t.Fatalf("tests=%d checks=%d", len(r.Tests), len(res.Checks))
	}
	for _, ck := range res.Checks {
		if !ck.Pass {
			t.Errorf("check %q failed: %s", ck.Name, ck.Detail)
		}
	}
	// Adding the same path again does not duplicate the rule.
	addTestForPath(r, res, "$.data.id")
	if len(r.Tests) != len(cases) {
		t.Errorf("duplicate rule added: %d", len(r.Tests))
	}
}
