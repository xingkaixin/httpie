package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xingkaixin/mygo-native-ui/ui/jsondiff"
)

// maxHistory is how many responses a request keeps.
const maxHistory = 20

// entry is a response in the history of a request, as the name of its file
// tells: when it was sent, and its status code.
type entry struct {
	At   time.Time
	Code int
}

func (e entry) file() string {
	return fmt.Sprintf("%d-%d.json", e.At.UnixNano(), e.Code)
}

func (e entry) label() string {
	return fmt.Sprintf("%s · %d", e.At.Format("1月2日 15:04:05"), e.Code)
}

func (e entry) is(res *response) bool {
	return res != nil && res.At.UnixNano() == e.At.UnixNano()
}

// entries returns the history kept in dir, the newest first.
func entries(dir string) []entry {
	files, _ := os.ReadDir(dir)
	var out []entry
	for _, f := range files {
		at, code, _ := strings.Cut(strings.TrimSuffix(f.Name(), ".json"), "-")
		nano, err := strconv.ParseInt(at, 10, 64)
		c, err2 := strconv.Atoi(code)
		if err == nil && err2 == nil {
			out = append(out, entry{time.Unix(0, nano), c})
		}
	}
	slices.SortFunc(out, func(a, b entry) int { return b.At.Compare(a.At) })
	return out
}

// saveResponse adds res to the history in dir, dropping the oldest past
// maxHistory. The file has the request as sent, credentials and all.
func saveResponse(dir string, res *response) (entry, bool) {
	e := entry{res.At, res.Code}
	data, err := json.Marshal(res)
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, e.file()), data, 0o600)
	}
	if err != nil {
		log.Printf("save response: %v", err)
		return e, false
	}
	if old := entries(dir); len(old) > maxHistory {
		for _, o := range old[maxHistory:] {
			os.Remove(filepath.Join(dir, o.file()))
		}
	}
	return e, true
}

func readResponse(path string) *response {
	data, err := os.ReadFile(path)
	res := &response{}
	if err != nil || json.Unmarshal(data, res) != nil {
		return nil
	}
	return res
}

// pruneResponses removes from dir the histories of requests ws no longer
// has. It runs at start, as deleting a request can be undone.
func pruneResponses(dir string, ws *workspace) {
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		id, single := strings.CutSuffix(f.Name(), ".json")
		path := filepath.Join(dir, f.Name())
		if _, r := ws.find(id); r == nil {
			os.RemoveAll(path)
			continue
		}
		// Before the history, a request kept its last response in a file
		// named by its ID.
		if single {
			if res := readResponse(path); res != nil {
				saveResponse(filepath.Join(dir, id), res)
			}
			os.Remove(path)
		}
	}
}

// comparison is a response of the history against the one shown.
type comparison struct {
	id       string
	old, new entry
	diff     *jsondiff.Diff
	state    jsondiff.State
}

// diffDoc returns res as compare shows it: its status, headers and body,
// as JSON when it is.
func diffDoc(res *response) []byte {
	headers := map[string]string{}
	for _, h := range res.Headers {
		name := strings.ToLower(h.Name)
		if v, ok := headers[name]; ok {
			h.Value = v + ", " + h.Value
		}
		headers[name] = h.Value
	}
	var body any = res.Body
	if res.JSON && !res.Truncated {
		body = json.RawMessage(res.Body)
	}
	data, _ := json.Marshal(struct {
		Status  string            `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    any               `json:"body"`
	}{res.Status, headers, body})
	return data
}

// compare shows how the response e of the history of the request id
// differs from cur, the one shown.
func (a *app) compare(id string, e entry, cur *response) {
	go func() {
		old := readResponse(filepath.Join(a.resDir, id, e.file()))
		if old == nil {
			a.win.Update(func() { a.notice = "无法读取这条历史响应" })
			return
		}
		cmp := &comparison{id: id, old: e, new: entry{cur.At, cur.Code}, diff: jsondiff.Compare(diffDoc(old), diffDoc(cur))}
		a.win.Update(func() { a.cmp, a.resTab = cmp, resBody })
	}()
}
