package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

type kv struct {
	On    bool   `json:"on"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type request struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Method   string `json:"method"`
	URL      string `json:"url"`
	Params   []kv   `json:"params,omitempty"`
	Headers  []kv   `json:"headers,omitempty"`
	BodyType string `json:"bodyType"`
	Body     string `json:"body,omitempty"`
	Form     []kv   `json:"form,omitempty"`
	AuthType string `json:"authType"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

type collection struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Open     bool       `json:"open"`
	Requests []*request `json:"requests"`
}

type workspace struct {
	Collections []*collection `json:"collections"`
	Selected    string        `json:"selected"`
	Split       float32       `json:"split"`
}

const (
	bodyNone = "None"
	bodyJSON = "JSON"
	bodyText = "Text"
	bodyForm = "Form"

	authNone   = "None"
	authBasic  = "Basic"
	authBearer = "Bearer"
)

var (
	methods   = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	bodyTypes = []string{bodyNone, bodyJSON, bodyText, bodyForm}
	authTypes = []string{authNone, authBasic, authBearer}
	idSeq     atomic.Int64
)

func newID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(idSeq.Add(1), 36)
}

func newRequest(name, method, url string) *request {
	return &request{ID: newID(), Name: name, Method: method, URL: url, BodyType: bodyNone, AuthType: authNone}
}

func sampleWorkspace() *workspace {
	post := newRequest("Echo JSON", "POST", "https://httpbin.org/post")
	post.BodyType = bodyJSON
	post.Body = "{\n  \"name\": \"MyGo\",\n  \"native\": true,\n  \"stars\": 42\n}"
	get := newRequest("Query params", "GET", "https://httpbin.org/get")
	get.Params = []kv{{On: true, Key: "lang", Value: "go"}, {On: true, Key: "page", Value: "1"}}
	repo := newRequest("GitHub repo", "GET", "https://api.github.com/repos/egoist/mygo")
	repo.Headers = []kv{{On: true, Key: "Accept", Value: "application/vnd.github+json"}}
	col := &collection{ID: newID(), Name: "示例", Open: true, Requests: []*request{get, post, repo}}
	return &workspace{Collections: []*collection{col}, Selected: get.ID, Split: 460}
}

func loadWorkspace(path string) (*workspace, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return sampleWorkspace(), nil
	}
	if err != nil {
		return nil, err
	}
	ws := &workspace{}
	if err := json.Unmarshal(data, ws); err != nil {
		return nil, err
	}
	return ws, nil
}

func (ws *workspace) find(id string) (*collection, *request) {
	for _, col := range ws.Collections {
		for _, r := range col.Requests {
			if r.ID == id {
				return col, r
			}
		}
	}
	return nil, nil
}

func enabled(rows []kv) []kv {
	var out []kv
	for _, r := range rows {
		if r.On && r.Key != "" {
			out = append(out, r)
		}
	}
	return out
}
