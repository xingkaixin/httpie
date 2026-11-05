package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

// check is how a response met a rule of the request's Tests: a rule's
// key names what it checks, as status, time, header Name, body or a JSON
// path, and its value what is expected of it, as "== 200", "2xx",
// "< 500", "contains json", "exists" or "matches ^ok$", "==" when no
// operator starts it.
type check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// checkSubjects and checkValues are what the Tests table suggests.
var checkSubjects = []string{"status", "time", "header ", "body", "$."}

func checkValues(key string) []string {
	switch {
	case key == "status":
		return []string{"2xx", "== 200", "!= 500"}
	case key == "time":
		return []string{"< 500", "< 1000"}
	case strings.HasPrefix(key, "header "):
		return []string{"exists", "contains json", "== "}
	case key == "body":
		return []string{"contains ", "matches "}
	}
	return []string{"exists", "== ", "!= null", "contains ", "> 0"}
}

var operators = []string{"!contains", "contains", "!exists", "exists", "matches", "==", "!=", "<=", ">=", "<", ">"}

var statusClass = regexp.MustCompile(`^[1-5]xx$`)

func runChecks(rules []kvtable.Pair, res *response) []check {
	var out []check
	for _, r := range enabled(rules) {
		c := check{Name: strings.TrimSpace(r.Key + " " + r.Value)}
		op, want := parseExpectation(r.Value)
		actual, found, err := subject(strings.TrimSpace(r.Key), res)
		switch {
		case err != nil:
			c.Detail = err.Error()
		case op == "exists" || op == "!exists":
			c.Pass = found == (op == "exists")
			if !c.Pass {
				c.Detail = "实际：" + map[bool]string{true: "存在", false: "不存在"}[found]
			}
		case !found:
			c.Detail = "实际：不存在"
		default:
			c.Pass, err = compare(op, actual, want)
			if err != nil {
				c.Detail = err.Error()
			} else if !c.Pass {
				c.Detail = "实际：" + shorten(actual)
			}
		}
		out = append(out, c)
	}
	return out
}

func parseExpectation(v string) (op, want string) {
	v = strings.TrimSpace(v)
	for _, op := range operators {
		if rest, ok := strings.CutPrefix(v, op); ok && (rest == "" || rest[0] == ' ' || !isWordOp(op)) {
			return op, strings.TrimSpace(rest)
		}
	}
	return "==", v
}

func isWordOp(op string) bool {
	return op[len(op)-1] >= 'a' && op[len(op)-1] <= 'z'
}

// subject returns what key names in res, as text: a JSON value as the
// server wrote it, compacted.
func subject(key string, res *response) (string, bool, error) {
	switch {
	case key == "status":
		return strconv.Itoa(res.Code), true, nil
	case key == "time":
		return strconv.FormatInt(res.Duration.Milliseconds(), 10), true, nil
	case key == "body":
		return res.Body, true, nil
	case strings.HasPrefix(key, "header "):
		name := strings.TrimSpace(strings.TrimPrefix(key, "header "))
		var values []string
		for _, h := range res.Headers {
			if strings.EqualFold(h.Name, name) {
				values = append(values, h.Value)
			}
		}
		return strings.Join(values, ", "), len(values) > 0, nil
	case strings.HasPrefix(key, "$"):
		if !res.JSON || res.Truncated {
			return "", false, fmt.Errorf("响应不是完整的 JSON")
		}
		v, ok := lookup(json.RawMessage(res.Body), key)
		if !ok {
			return "", false, nil
		}
		var b bytes.Buffer
		if err := json.Compact(&b, v); err != nil {
			return string(v), true, nil
		}
		return b.String(), true, nil
	}
	return "", false, fmt.Errorf("不认识的检查对象：%s", key)
}

// compare tells whether actual, the text of a subject, is op want. A JSON
// string is compared by its text, unless want is JSON as well.
func compare(op, actual, want string) (bool, error) {
	text := actual
	var s string
	if json.Unmarshal([]byte(actual), &s) == nil {
		text = s
	}
	switch op {
	case "==", "!=":
		var eq bool
		if statusClass.MatchString(want) {
			eq = len(text) == 3 && text[0] == want[0]
		} else {
			eq = text == want || jsonEqual(actual, want)
		}
		return eq == (op == "=="), nil
	case "contains", "!contains":
		return strings.Contains(text, want) == (op == "contains"), nil
	case "matches":
		re, err := regexp.Compile(want)
		if err != nil {
			return false, fmt.Errorf("正则有误：%v", err)
		}
		return re.MatchString(text), nil
	}
	a, err1 := strconv.ParseFloat(text, 64)
	b, err2 := strconv.ParseFloat(strings.TrimSuffix(want, "ms"), 64)
	if err1 != nil || err2 != nil {
		return false, fmt.Errorf("%s 需要两边都是数字", op)
	}
	switch op {
	case "<":
		return a < b, nil
	case "<=":
		return a <= b, nil
	case ">":
		return a > b, nil
	}
	return a >= b, nil
}

func jsonEqual(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

func shorten(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}
