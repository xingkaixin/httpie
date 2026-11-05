package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

func TestImportPostman(t *testing.T) {
	data, err := os.ReadFile("testdata/postman.json")
	if err != nil {
		t.Fatal(err)
	}
	col, _, unknown, err := importPostman(data)
	if err != nil {
		t.Fatal(err)
	}
	if col.Name != "Shop" || col.Auth != (auth{AuthType: authBearer, Token: "{{token}}"}) || unknown != 1 ||
		!reflect.DeepEqual(col.Vars, []kvtable.Pair{{On: true, Key: "base", Value: "https://api.example.com", Kind: kindText}, {Key: "old", Value: "1", Kind: kindText}}) {
		t.Fatalf("collection %+v, %d unknown", col, unknown)
	}
	text := func(k, v string, on bool) kvtable.Pair { return kvtable.Pair{On: on, Key: k, Value: v, Kind: kindText} }
	want := []request{
		{
			Name: "Users / Get user", Method: "GET", URL: "{{base}}/users/:id?expand=1",
			Params:   []kvtable.Pair{{On: true, Key: "expand", Value: "1"}, text("debug", "true", false)},
			PathVars: []kvtable.Pair{{On: true, Key: "id", Value: "42"}},
			Headers:  []kvtable.Pair{text("Accept", "application/json", true), text("X-Off", "1", false)},
			BodyType: bodyNone, auth: auth{AuthType: authBasic, User: "kev", Password: "pw"},
		},
		{
			Name: "Create order", Method: "POST", URL: "{{base}}/orders", BodyType: bodyJSON, Body: `{"n": 1}`,
			auth: auth{AuthType: authAPIKey, Key: "X-Key", Token: "s3", In: inQuery},
		},
		{
			Name: "Upload", Method: "POST", URL: "{{base}}/files", BodyType: bodyMultipart, auth: auth{AuthType: authNone},
			Form: []kvtable.Pair{text("note", "hi", true), {On: true, Key: "doc", Value: "/tmp/a.pdf", Kind: kindFile}},
		},
		{
			Name: "Login", Method: "POST", URL: "{{base}}/login", BodyType: bodyForm, auth: auth{AuthType: authNone},
			Form: []kvtable.Pair{text("user", "kev", true)},
		},
	}
	if len(col.Requests) != len(want) {
		t.Fatalf("%d requests", len(col.Requests))
	}
	for i, r := range col.Requests {
		w := want[i]
		w.ID, w.Follow = r.ID, true
		if !reflect.DeepEqual(*r, w) {
			t.Errorf("request %d\n got %+v\nwant %+v", i, *r, w)
		}
	}

	_, env, _, err := importPostman([]byte(`{"name": "Prod", "values": [{"key": "token", "value": "t", "type": "secret", "enabled": true}, {"key": "x", "value": "y", "enabled": false}]}`))
	if err != nil || env.Name != "Prod" || !reflect.DeepEqual(env.Vars, []kvtable.Pair{{On: true, Key: "token", Value: "t", Kind: kindSecret}, text("x", "y", false)}) {
		t.Errorf("environment %+v, %v", env, err)
	}
}
