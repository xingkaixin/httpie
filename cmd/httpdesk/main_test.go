package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	res := send(context.Background(), requestArgs(env.resolve(r)))
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
