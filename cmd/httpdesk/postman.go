package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

// pmItem is a request of a Postman collection, or a folder of them, as
// v2.0 and v2.1 write them.
type pmItem struct {
	Name    string                `json:"name"`
	Item    []pmItem              `json:"item"`
	Request *pmRequest            `json:"request"`
	Auth    *pmAuth               `json:"auth"`
	Info    struct{ Name string } `json:"info"`
	Var     []pmPair              `json:"variable"`
	Values  []pmPair              `json:"values"`
}

type pmRequest struct {
	Method string          `json:"method"`
	Header []pmPair        `json:"header"`
	URL    json.RawMessage `json:"url"`
	Body   *pmBody         `json:"body"`
	Auth   *pmAuth         `json:"auth"`
}

type pmPair struct {
	Key      string          `json:"key"`
	Value    any             `json:"value"`
	Disabled bool            `json:"disabled"`
	Enabled  *bool           `json:"enabled"`
	Type     string          `json:"type"`
	Src      json.RawMessage `json:"src"`
}

type pmBody struct {
	Mode       string   `json:"mode"`
	Raw        string   `json:"raw"`
	URLEncoded []pmPair `json:"urlencoded"`
	FormData   []pmPair `json:"formdata"`
	File       struct {
		Src string `json:"src"`
	} `json:"file"`
	GraphQL struct {
		Query     string `json:"query"`
		Variables string `json:"variables"`
	} `json:"graphql"`
	Options struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

// pmAuth has its parameters as v2.1 writes them, a list of keys and
// values, or as v2.0 does, an object.
type pmAuth struct {
	Type   string                     `json:"type"`
	Params map[string]json.RawMessage `json:"-"`
}

func (a *pmAuth) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	json.Unmarshal(raw["type"], &a.Type)
	a.Params = map[string]json.RawMessage{}
	var list []pmPair
	if json.Unmarshal(raw[a.Type], &list) == nil {
		for _, p := range list {
			a.Params[p.Key], _ = json.Marshal(p.Value)
		}
	} else {
		json.Unmarshal(raw[a.Type], &a.Params)
	}
	return nil
}

func (a *pmAuth) param(name string) string {
	var s string
	if json.Unmarshal(a.Params[name], &s) != nil {
		return string(a.Params[name])
	}
	return s
}

func (p pmPair) text() string {
	switch v := p.Value.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	data, _ := json.Marshal(p.Value)
	return string(data)
}

func (p pmPair) on() bool {
	if p.Enabled != nil {
		return *p.Enabled
	}
	return !p.Disabled
}

func pmPairs(list []pmPair) []kvtable.Pair {
	var out []kvtable.Pair
	for _, p := range list {
		if p.Key != "" {
			out = append(out, kvtable.Pair{On: p.on(), Key: p.Key, Value: p.text(), Kind: kindText})
		}
	}
	return out
}

// rawContentTypes are the types Postman sends a raw body of a language
// as, which a Text body would send as text/plain.
var rawContentTypes = map[string]string{"xml": "application/xml", "html": "text/html", "javascript": "application/javascript"}

// importPostman reads a collection or an environment Postman exports,
// and tells how many requests had an authentication it does not know,
// which go without.
func importPostman(data []byte) (col *collection, env *environment, unknownAuth int, err error) {
	var doc pmItem
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, 0, err
	}
	if doc.Item == nil {
		if doc.Values == nil {
			return nil, nil, 0, errors.New("不是 Postman 集合或环境")
		}
		env = &environment{ID: newID(), Name: doc.Name}
		for _, v := range doc.Values {
			kind := kindText
			if v.Type == "secret" {
				kind = kindSecret
			}
			env.Vars = append(env.Vars, kvtable.Pair{On: v.on(), Key: v.Key, Value: v.text(), Kind: kind})
		}
		return nil, env, 0, nil
	}
	col = &collection{ID: newID(), Name: doc.Info.Name, Open: true, Auth: auth{AuthType: authNone}, Vars: pmPairs(doc.Var)}
	if doc.Auth != nil {
		var ok bool
		if col.Auth, ok = pmAuthOf(doc.Auth); !ok || col.Auth.AuthType == authInherit {
			col.Auth = auth{AuthType: authNone}
		}
	}
	var walk func(items []pmItem, prefix string, folderAuth *auth)
	walk = func(items []pmItem, prefix string, folderAuth *auth) {
		for _, it := range items {
			if it.Request == nil {
				inner := folderAuth
				if it.Auth != nil {
					if a, ok := pmAuthOf(it.Auth); ok && a.AuthType != authInherit {
						inner = &a
					}
				}
				walk(it.Item, prefix+it.Name+" / ", inner)
				continue
			}
			r := pmRequestOf(it.Request)
			r.Name = prefix + it.Name
			switch a, ok := pmAuthOf(it.Request.Auth); {
			case !ok:
				unknownAuth++
				r.auth = auth{AuthType: authNone}
			case a.AuthType == authInherit && folderAuth != nil:
				r.auth = *folderAuth
			default:
				r.auth = a
			}
			col.Requests = append(col.Requests, r)
		}
	}
	walk(doc.Item, "", nil)
	return col, nil, unknownAuth, nil
}

// pmAuthOf returns the auth of a, which is inherited when nil, or false
// when the app has none of its type.
func pmAuthOf(a *pmAuth) (auth, bool) {
	if a == nil {
		return auth{AuthType: authInherit}, true
	}
	switch a.Type {
	case "inherit":
		return auth{AuthType: authInherit}, true
	case "noauth":
		return auth{AuthType: authNone}, true
	case "basic":
		return auth{AuthType: authBasic, User: a.param("username"), Password: a.param("password")}, true
	case "digest":
		return auth{AuthType: authDigest, User: a.param("username"), Password: a.param("password")}, true
	case "bearer":
		return auth{AuthType: authBearer, Token: a.param("token")}, true
	case "apikey":
		in := inHeader
		if a.param("in") == "query" {
			in = inQuery
		}
		return auth{AuthType: authAPIKey, Key: a.param("key"), Token: a.param("value"), In: in}, true
	}
	return auth{}, false
}

func pmRequestOf(p *pmRequest) *request {
	r := newRequest("", strings.ToUpper(p.Method), "")
	if r.Method == "" {
		r.Method = "GET"
	}
	var u struct {
		Raw      string   `json:"raw"`
		Query    []pmPair `json:"query"`
		Variable []pmPair `json:"variable"`
	}
	if json.Unmarshal(p.URL, &r.URL) != nil && json.Unmarshal(p.URL, &u) == nil {
		r.URL = u.Raw
	}
	var off []kvtable.Pair
	for _, q := range pmPairs(u.Query) {
		if !q.On {
			off = append(off, q)
		}
	}
	r.Params, r.PathVars = parse(r.URL, off), pathVars(r.URL, pmPairs(u.Variable))
	r.Headers = pmPairs(p.Header)
	if b := p.Body; b != nil {
		switch b.Mode {
		case "raw":
			r.BodyType, r.Body = bodyText, b.Raw
			lang := b.Options.Raw.Language
			if lang == "json" {
				r.BodyType = bodyJSON
			} else if t, ok := rawContentTypes[lang]; ok && headerIndex(r.Headers, "Content-Type") < 0 {
				r.Headers = append(r.Headers, kvtable.Pair{On: true, Key: "Content-Type", Value: t})
			}
		case "urlencoded":
			r.BodyType, r.Form = bodyForm, pmPairs(b.URLEncoded)
		case "formdata":
			r.BodyType = bodyMultipart
			for _, f := range b.FormData {
				field := kvtable.Pair{On: f.on(), Key: f.Key, Value: f.text(), Kind: kindText}
				if f.Type == "file" {
					field.Kind, field.Value = kindFile, pmSrc(f.Src)
				}
				r.Form = append(r.Form, field)
			}
		case "file":
			r.BodyType, r.BodyFile = bodyFile, b.File.Src
		case "graphql":
			vars := json.RawMessage(b.GraphQL.Variables)
			if !json.Valid(vars) {
				vars = json.RawMessage("{}")
			}
			body, _ := json.MarshalIndent(map[string]any{"query": b.GraphQL.Query, "variables": vars}, "", "  ")
			r.BodyType, r.Body = bodyJSON, string(body)
		}
	}
	return r
}

// pmSrc returns the path of a form's file, of which Postman may keep a
// list.
func pmSrc(src json.RawMessage) string {
	var path string
	if json.Unmarshal(src, &path) == nil {
		return path
	}
	var paths []string
	if json.Unmarshal(src, &paths) == nil && len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// importFiles asks for Postman exports and adds what they hold to the
// workspace.
func (a *app) importFiles() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.win, Title: "导入 Postman 集合或环境", Multiple: true,
			Filters: []mygo.FileFilter{{Name: "Postman", Extensions: []string{"json"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		type result struct {
			col         *collection
			env         *environment
			unknownAuth int
			err         error
		}
		results := make([]result, len(paths))
		for i, path := range paths {
			data, err := os.ReadFile(path)
			if err == nil {
				results[i].col, results[i].env, results[i].unknownAuth, err = importPostman(data)
			}
			if err != nil {
				results[i].err = fmt.Errorf("%s：%w", path, err)
			}
		}
		a.win.Update(func() {
			var notes []string
			for _, res := range results {
				switch {
				case res.err != nil:
					notes = append(notes, "无法导入 "+res.err.Error())
				case res.env != nil:
					a.ws.Environments = append(a.ws.Environments, res.env)
					notes = append(notes, fmt.Sprintf("已导入环境“%s”", res.env.Name))
				default:
					a.ws.Collections = append(a.ws.Collections, res.col)
					if len(res.col.Requests) > 0 {
						a.ws.Selected = res.col.Requests[0].ID
					}
					note := fmt.Sprintf("已导入“%s”的 %d 个请求", res.col.Name, len(res.col.Requests))
					if res.unknownAuth > 0 {
						note += fmt.Sprintf("，其中 %d 个的认证方式不支持，已设为无", res.unknownAuth)
					}
					notes = append(notes, note)
				}
			}
			a.notice = strings.Join(notes, "；")
		})
	}()
}
