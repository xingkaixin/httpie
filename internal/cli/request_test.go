package cli

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestUpstreamOptionInventory(t *testing.T) {
	data, err := os.ReadFile("../../testdata/options.json")
	if err != nil {
		t.Fatal(err)
	}
	var upstream []struct {
		Options []string `json:"options"`
	}
	if err = json.Unmarshal(data, &upstream); err != nil {
		t.Fatal(err)
	}
	for _, s := range upstream {
		for _, name := range s.Options {
			found := false
			for _, g := range optionSpecs {
				if name == "--"+g.name || g.short != "" && name == "-"+g.short {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("missing option %s", name)
			}
		}
	}
}

func TestRequestContract(t *testing.T) {
	cases := []struct {
		args                  []string
		method, url, body, ct string
	}{
		{[]string{"example.org"}, "GET", "http://example.org/", "", ""},
		{[]string{":3000/path", "x==a b", "x==c"}, "GET", "http://localhost:3000/path?x=a+b&x=c", "", ""},
		{[]string{"example.org", "a=1", "b:=true"}, "POST", "http://example.org/", `{"a": "1", "b": true}`, "application/json"},
		{[]string{"--form", "example.org", "a=1", "a=2", "x:=true"}, "POST", "http://example.org/", "a=1&a=2&x=True", "application/x-www-form-urlencoded; charset=utf-8"},
		{[]string{"PUT", "example.org/a/../b", "x[]=one", "x[3]:=4"}, "PUT", "http://example.org/b", `{"x": ["one", null, null, 4]}`, "application/json"},
		{[]string{"example.org", "[]:=1", "[2][x]=y"}, "POST", "http://example.org/", `[1, null, {"x": "y"}]`, "application/json"},
		{[]string{"--json", "--no-form", "example.org"}, "GET", "http://example.org/", "", ""},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			o, err := parseOptions(c.args)
			if err != nil {
				t.Fatal(err)
			}
			r, close, err := buildRequest(o, "http", strings.NewReader(""), true)
			if err != nil {
				t.Fatal(err)
			}
			defer close()
			var body []byte
			if r.Body != nil {
				body, err = io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			got := []string{r.Method, r.URL.String(), string(body), r.Header.Get("Content-Type")}
			want := []string{c.method, c.url, c.body, c.ct}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %#v; want %#v", got, want)
			}
		})
	}
}
func TestRequestSourceConflicts(t *testing.T) {
	for _, args := range [][]string{{"example.org", "x=1"}, {"--raw=x", "example.org"}} {
		o, err := parseOptions(args)
		if err != nil {
			t.Fatal(err)
		}
		_, cleanup, err := buildRequest(o, "http", strings.NewReader(""), false)
		cleanup()
		if err == nil {
			t.Errorf("expected conflict for %v", args)
		}
	}
}
