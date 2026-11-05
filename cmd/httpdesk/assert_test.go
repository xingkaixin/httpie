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
