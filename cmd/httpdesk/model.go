package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"

	"httpie/internal/core"
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
	auth
	BodyFile string  `json:"bodyFile,omitempty"`
	Follow   bool    `json:"follow,omitempty"`
	Insecure bool    `json:"insecure,omitempty"`
	Timeout  float64 `json:"timeout,omitempty"`
	// Extract sets variables of the environment from the response, each
	// key from the JSON path of its value.
	Extract []kvtable.Pair `json:"extract,omitempty"`
}

type auth struct {
	AuthType string `json:"authType"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

type collection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Open bool   `json:"open"`
	// Auth is that of the requests inheriting it.
	Auth     auth       `json:"auth"`
	Requests []*request `json:"requests"`
}

type workspace struct {
	Collections  []*collection  `json:"collections"`
	Environments []*environment `json:"environments,omitempty"`
	Env          string         `json:"env,omitempty"`
	// Cookies is the jar all requests share, whatever the environment.
	Cookies  []core.Cookie `json:"cookies,omitempty"`
	Selected string        `json:"selected"`
	Split    float32       `json:"split"`
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

	authInherit = "Inherit"
	authNone    = "None"
	authBasic   = "Basic"
	authDigest  = "Digest"
	authBearer  = "Bearer"
)

var (
	methods          = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	bodyTypes        = []string{bodyNone, bodyJSON, bodyText, bodyForm, bodyMultipart, bodyFile}
	authTypes        = []string{authNone, authBasic, authDigest, authBearer}
	requestAuthTypes = append([]string{authInherit}, authTypes...)
	idSeq            atomic.Int64
)

func newID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(idSeq.Add(1), 36)
}

func newRequest(name, method, url string) *request {
	return &request{ID: newID(), Name: name, Method: method, URL: url, BodyType: bodyNone, auth: auth{AuthType: authInherit}, Follow: true}
}

func sampleWorkspace() *workspace {
	post := newRequest("Echo JSON", "POST", "https://httpbin.org/post")
	post.BodyType = bodyJSON
	post.Body = "{\n  \"name\": \"MyGo\",\n  \"native\": true,\n  \"stars\": 42\n}"
	get := newRequest("Query params", "GET", "https://httpbin.org/get?lang=go&page=1")
	get.Params = parse(get.URL, nil)
	repo := newRequest("GitHub repo", "GET", "https://api.github.com/repos/egoist/mygo")
	repo.Headers = []kvtable.Pair{{On: true, Key: "Accept", Value: "application/vnd.github+json"}}
	col := &collection{ID: newID(), Name: "示例", Open: true, Auth: auth{AuthType: authNone}, Requests: []*request{get, post, repo}}
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
	for _, col := range ws.Collections {
		// Collections saved before they had auth have none.
		if col.Auth.AuthType == "" {
			col.Auth.AuthType = authNone
		}
		// Workspaces saved before Params followed the URL's query sent
		// both, the query first.
		for _, r := range col.Requests {
			if !slices.Equal(enabled(parse(r.URL, nil)), enabled(r.Params)) {
				r.Params = append(parse(r.URL, nil), r.Params...)
				r.URL = withQuery(r.URL, r.Params)
			}
		}
	}
	return ws, nil
}

// saveResponse keeps res in dir as the last response of the request id,
// apart from the workspace, which is saved each second.
func saveResponse(dir, id string, res *response) {
	data, err := json.Marshal(res)
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err == nil {
		// It has the request as sent, credentials and all.
		err = os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600)
	}
	if err != nil {
		log.Printf("save response: %v", err)
	}
}

func readResponse(dir, id string) *response {
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	res := &response{}
	if err != nil || json.Unmarshal(data, res) != nil {
		return nil
	}
	return res
}

// pruneResponses removes from dir the responses of requests ws no longer
// has. It runs at start, as deleting a request can be undone.
func pruneResponses(dir string, ws *workspace) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if _, r := ws.find(strings.TrimSuffix(e.Name(), ".json")); r == nil {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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

// effective returns r as sent, with col's auth where r inherits it.
func (col *collection) effective(r *request) *request {
	if r.AuthType != authInherit {
		return r
	}
	cp := *r
	cp.auth = col.Auth
	return &cp
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
