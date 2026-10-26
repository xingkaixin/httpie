package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		":3000/api":        "http://localhost:3000/api",
		"localhost:8080":   "http://localhost:8080",
		"httpbin.org/get":  "https://httpbin.org/get",
		"http://a.com/x?y": "http://a.com/x?y",
	} {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSendRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"q":"`+r.URL.Query().Get("q")+`","auth":"`+r.Header.Get("Authorization")+`","body":`+string(body)+`}`)
	}))
	defer srv.Close()

	r := newRequest("t", "POST", srv.URL)
	r.Params = []kv{{On: true, Key: "q", Value: "go"}, {On: false, Key: "skip", Value: "x"}}
	r.AuthType, r.Token = authBearer, "abc"
	r.BodyType, r.Body = bodyJSON, `{"n":1}`
	req, err := buildRequest(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	res := do(srv.Client(), req)
	want := "{\n  \"q\": \"go\",\n  \"auth\": \"Bearer abc\",\n  \"body\": {\n    \"n\": 1\n  }\n}"
	if res.Code != 200 || !res.JSON || res.Body != want {
		t.Errorf("response %d json=%v body %q", res.Code, res.JSON, res.Body)
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
