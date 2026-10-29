package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
)

type request struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Method   string         `json:"method"`
	URL      string         `json:"url"`
	Params   []kvtable.Pair `json:"params,omitempty"`
	Headers  []kvtable.Pair `json:"headers,omitempty"`
	BodyType string         `json:"bodyType"`
	Body     string         `json:"body,omitempty"`
	Form     []kvtable.Pair `json:"form,omitempty"`
	AuthType string         `json:"authType"`
	User     string         `json:"user,omitempty"`
	Password string         `json:"password,omitempty"`
	Token    string         `json:"token,omitempty"`
	BodyFile string         `json:"bodyFile,omitempty"`
	Follow   bool           `json:"follow,omitempty"`
	Insecure bool           `json:"insecure,omitempty"`
	Timeout  float64        `json:"timeout,omitempty"`
}

type collection struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Open     bool       `json:"open"`
	Requests []*request `json:"requests"`
}

type workspace struct {
	Collections  []*collection  `json:"collections"`
	Environments []*environment `json:"environments,omitempty"`
	Env          string         `json:"env,omitempty"`
	Selected     string         `json:"selected"`
	Split        float32        `json:"split"`
}

const (
	bodyNone      = "None"
	bodyJSON      = "JSON"
	bodyText      = "Text"
	bodyForm      = "Form"
	bodyMultipart = "Multipart"
	bodyFile      = "File"

	kindText = "Text"
	kindFile = "File"

	authNone   = "None"
	authBasic  = "Basic"
	authDigest = "Digest"
	authBearer = "Bearer"
)

var (
	methods   = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	bodyTypes = []string{bodyNone, bodyJSON, bodyText, bodyForm, bodyMultipart, bodyFile}
	authTypes = []string{authNone, authBasic, authDigest, authBearer}
	idSeq     atomic.Int64
)

func newID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(idSeq.Add(1), 36)
}

func newRequest(name, method, url string) *request {
	return &request{ID: newID(), Name: name, Method: method, URL: url, BodyType: bodyNone, AuthType: authNone, Follow: true}
}

func sampleWorkspace() *workspace {
	post := newRequest("Echo JSON", "POST", "https://httpbin.org/post")
	post.BodyType = bodyJSON
	post.Body = "{\n  \"name\": \"MyGo\",\n  \"native\": true,\n  \"stars\": 42\n}"
	get := newRequest("Query params", "GET", "https://httpbin.org/get")
	get.Params = []kvtable.Pair{{On: true, Key: "lang", Value: "go"}, {On: true, Key: "page", Value: "1"}}
	repo := newRequest("GitHub repo", "GET", "https://api.github.com/repos/egoist/mygo")
	repo.Headers = []kvtable.Pair{{On: true, Key: "Accept", Value: "application/vnd.github+json"}}
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

func enabled(rows []kvtable.Pair) []kvtable.Pair {
	var out []kvtable.Pair
	for _, r := range rows {
		if r.On && r.Key != "" {
			out = append(out, r)
		}
	}
	return out
}
