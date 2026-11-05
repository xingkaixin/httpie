package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

func TestSendRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"q":"`+r.URL.Query().Get("q")+`","auth":"`+r.Header.Get("Authorization")+`","body":`+string(body)+`}`)
	}))
	defer srv.Close()

	env := &environment{Name: "test", Vars: []kvtable.Pair{{On: true, Key: "base", Value: srv.URL}, {On: true, Key: "token", Value: "abc", Kind: kindSecret}}}
	r := newRequest("t", "POST", "{{base}}")
	r.Params = []kvtable.Pair{{On: true, Key: "q", Value: "go"}, {On: false, Key: "skip", Value: "x"}}
	r.AuthType, r.Token = authBearer, "{{ token }}"
	r.BodyType, r.Body = bodyJSON, `{"n":1}`
	res := send(context.Background(), requestArgs(scope{env: env}.resolve(r)), nil, nil)
	want := "{\n  \"q\": \"go\",\n  \"auth\": \"Bearer abc\",\n  \"body\": {\n    \"n\": 1\n  }\n}"
	if res.Code != 200 || !res.JSON || res.Body != want {
		t.Errorf("response %d json=%v body %q err %q", res.Code, res.JSON, res.Body, res.Err)
	}
	if !strings.HasPrefix(res.Sent, "POST /?q=go HTTP/1.1\n") || !strings.HasSuffix(res.Sent, "\n\n{\"n\":1}") {
		t.Errorf("sent %q", res.Sent)
	}
}

func TestRequestArgs(t *testing.T) {
	r := newRequest("t", "PUT", "example.org")
	r.Headers = []kvtable.Pair{{On: true, Key: "X-Empty"}, {On: true, Key: "-a:b", Value: "v"}}
	r.BodyType, r.Body = bodyText, "hi"
	want := []string{"--follow", "--raw=hi", "--", "PUT", "example.org", "X-Empty;", `-a\:b:v`, "Content-Type:text/plain; charset=utf-8"}
	if got := requestArgs(r); !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}

	r = newRequest("t", "GET", "example.org/a?x=1&sig=a%2Bb&to=@kevin#top")
	r.Params = parse(r.URL, nil)
	want = []string{"--follow", "GET", "example.org/a#top", "x==1", "sig==a+b", `to==\@kevin`}
	if got := requestArgs(scope{}.resolve(r)); !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}

	env := &environment{Vars: []kvtable.Pair{{On: true, Key: "next", Value: "example.org/a?page=2"}}}
	r = newRequest("t", "GET", "{{next}}")
	want = []string{"--follow", "GET", "example.org/a?page=2"}
	if got := requestArgs(scope{env: env}.resolve(r)); !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

func TestLoadWorkspaceMergesQuery(t *testing.T) {
	merged := newRequest("merged", "GET", "example.org/a?x=1")
	merged.Params = []kvtable.Pair{{On: true, Key: "x", Value: "9"}, {Key: "off", Value: "0"}}
	synced := newRequest("synced", "GET", "example.org/b?k=v")
	synced.Params = []kvtable.Pair{{Key: "off", Value: "0"}, {On: true, Key: "k", Value: "v"}}
	data, _ := json.Marshal(&workspace{Collections: []*collection{{Requests: []*request{merged, synced}}}})
	path := filepath.Join(t.TempDir(), "workspace.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := loadWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	got := ws.Collections[0].Requests
	params := []kvtable.Pair{{On: true, Key: "x", Value: "1"}, {On: true, Key: "x", Value: "9"}, {Key: "off", Value: "0"}}
	if got[0].URL != "example.org/a?x=1&x=9" || !slices.Equal(got[0].Params, params) {
		t.Errorf("merged %q %v", got[0].URL, got[0].Params)
	}
	if got[1].URL != synced.URL || !slices.Equal(got[1].Params, synced.Params) {
		t.Errorf("synced %q %v", got[1].URL, got[1].Params)
	}
}

func TestJSONTokens(t *testing.T) {
	got := jsonTokens(`{"a": "b", "n": -1.5, "t": true, "z": null}`)
	kinds := []tokenKind{tokKey, tokString, tokKey, tokNumber, tokKey, tokLiteral, tokKey, tokNull}
	if len(got) != len(kinds) {
		t.Fatalf("tokens %v", got)
	}
	for i, k := range kinds {
		if got[i].kind != k {
			t.Errorf("token %d kind %d, want %d", i, got[i].kind, k)
		}
	}
}

func TestPathVars(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://example.org:8443/users/:id/posts/:post_id?x=:id", "https://example.org:8443/users/a%2Fb/posts/:post_id?x=:id"},
		{":3000/users/:id", ":3000/users/a%2Fb"},
		{"{{base}}/users/:id.json", "{{base}}/users/a%2Fb.json"},
	} {
		vars := pathVars(tc.url, []kvtable.Pair{{Key: "id", Value: "a/b"}})
		if got := fillPath(tc.url, vars); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.url, got, tc.want)
		}
	}
}

func TestCollectionInheritance(t *testing.T) {
	col := &collection{
		Name:    "api",
		Headers: []kvtable.Pair{{On: true, Key: "X-Team", Value: "{{team}}"}, {On: true, Key: "Accept", Value: "a"}, {On: true, Key: "X-Off", Value: "b"}},
		Vars:    []kvtable.Pair{{On: true, Key: "team", Value: "col"}, {On: true, Key: "host", Value: "col.example"}},
	}
	r := newRequest("t", "GET", "{{host}}")
	r.Headers = []kvtable.Pair{{On: true, Key: "accept", Value: "mine"}, {Key: "x-off"}}
	env := &environment{Vars: []kvtable.Pair{{On: true, Key: "host", Value: "env.example"}}}
	got := scope{env, col}.resolve(col.effective(r))
	want := []kvtable.Pair{{On: true, Key: "X-Team", Value: "col"}, {On: true, Key: "accept", Value: "mine"}, {Key: "x-off"}}
	if got.URL != "env.example" || !slices.Equal(got.Headers, want) {
		t.Errorf("url %q headers %v", got.URL, got.Headers)
	}
}

func TestTiming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	res := send(context.Background(), requestArgs(newRequest("t", "GET", srv.URL+"/old")), nil, nil)
	if len(res.Timing) != 2 || !strings.HasSuffix(res.Timing[1].Name, "/new") {
		t.Fatalf("timing %+v", res.Timing)
	}
	var names []string
	for _, p := range res.Timing[0].Phases {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"Connect", "Send", "Wait", "Receive"}) {
		t.Errorf("phases %v", names)
	}
}
