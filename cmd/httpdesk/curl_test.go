package main

import (
	"reflect"
	"testing"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

func TestParseCurl(t *testing.T) {
	on := func(k, v string) kvtable.Pair { return kvtable.Pair{On: true, Key: k, Value: v} }
	tests := []struct {
		name, cmd string
		want      request
	}{
		{
			"chrome",
			`curl 'https://api.example.com/v1/items?page=2' \
  -H 'accept: application/json' \
  -H 'content-type: application/json' \
  -b 'sid=abc; theme=dark' \
  --data-raw $'{"name":"it\'s","note":"a\\nb\u0021"}'`,
			request{
				Method: "POST", URL: "https://api.example.com/v1/items?page=2", Params: []kvtable.Pair{on("page", "2")},
				Headers:  []kvtable.Pair{on("accept", "application/json"), on("content-type", "application/json"), on("Cookie", "sid=abc; theme=dark")},
				BodyType: bodyJSON, Body: `{"name":"it's","note":"a\nb!"}`, AuthType: authNone,
			},
		},
		{
			"firefox",
			`curl 'https://example.com/login' -X POST -H 'User-Agent: Mozilla/5.0' -H 'Content-Type: application/x-www-form-urlencoded' --data-raw 'user=ada&pass=x%21'`,
			request{
				Method: "POST", URL: "https://example.com/login",
				Headers:  []kvtable.Pair{on("User-Agent", "Mozilla/5.0"), on("Content-Type", "application/x-www-form-urlencoded")},
				BodyType: bodyText, Body: "user=ada&pass=x%21", AuthType: authNone,
			},
		},
		{
			"safari",
			"curl 'https://example.com/api' \\\r\n-X 'PUT' \\\r\n-H 'Content-Type: application/json' \\\r\n--data-binary '{\"a\":1}'",
			request{
				Method: "PUT", URL: "https://example.com/api", Headers: []kvtable.Pair{on("Content-Type", "application/json")},
				BodyType: bodyJSON, Body: `{"a":1}`, AuthType: authNone,
			},
		},
		{
			"postman",
			`curl --location 'https://example.com/upload' \
--header 'Authorization: Bearer t0k' \
--form 'name="Ada"' \
--form 'file=@"/tmp/a b.png"'`,
			request{
				Method: "POST", URL: "https://example.com/upload", Headers: []kvtable.Pair{on("Authorization", "Bearer t0k")},
				BodyType: bodyMultipart, Form: []kvtable.Pair{{On: true, Key: "name", Value: "Ada", Kind: kindText}, {On: true, Key: "file", Value: "/tmp/a b.png", Kind: kindFile}},
				AuthType: authNone, Follow: true,
			},
		},
		{
			"docs",
			`curl -sSL -kXDELETE -u ada:s3cret --digest -m 2.5 "https://example.com/items/1" -d x=1 -d "y=\"2\"" -H 'X-Empty;' -H 'Accept:'`,
			request{
				Method: "DELETE", URL: "https://example.com/items/1",
				Headers:  []kvtable.Pair{on("X-Empty", ""), on("Content-Type", "application/x-www-form-urlencoded")},
				BodyType: bodyText, Body: `x=1&y="2"`, AuthType: authDigest, User: "ada", Password: "s3cret",
				Follow: true, Insecure: true, Timeout: 2.5,
			},
		},
		{
			"json file",
			`curl --json @body.json example.org`,
			request{
				Method: "POST", URL: "example.org", Headers: []kvtable.Pair{on("Content-Type", "application/json"), on("Accept", "application/json")},
				BodyType: bodyFile, BodyFile: "body.json", AuthType: authNone,
			},
		},
		{
			"head",
			`curl -I --url example.org/a`,
			request{Method: "HEAD", URL: "example.org/a", BodyType: bodyNone, AuthType: authNone},
		},
	}
	for _, tt := range tests {
		got, err := parseCurl(tt.cmd)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		got.ID = ""
		if !reflect.DeepEqual(*got, tt.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.name, *got, tt.want)
		}
	}
	for _, cmd := range []string{`curl -H 'a: b`, `curl -X POST`} {
		if _, err := parseCurl(cmd); err == nil {
			t.Errorf("%s: no error", cmd)
		}
	}
}
