package main

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"time"
	"uuid"

	"github.com/egoist/mygo/ui"
	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
	"github.com/xingkaixin/mygo-native-ui/ui/templatefield"
)

const kindSecret = "Secret"

var varKinds = []kvtable.Kind{{Name: kindText}, {Name: kindSecret, Cell: kvtable.Secret}}

// variable matches a variable as templatefield writes one, or a dynamic
// one, which templatefield does not know.
var variable = regexp.MustCompile(`\{\{ *(\$?[\w.-]+) *\}\}`)

// dynamic are the variables fill makes anew at each use, with an
// environment or without.
var dynamic = map[string]func() string{
	"$uuid":         func() string { return uuid.New().String() },
	"$timestamp":    func() string { return strconv.FormatInt(time.Now().Unix(), 10) },
	"$isoTimestamp": func() string { return time.Now().UTC().Format(time.RFC3339) },
}

type environment struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Vars []kvtable.Pair `json:"vars,omitempty"`
}

// scope is what defines the variables of a request: its environment,
// then its collection, either of which may be nil.
type scope struct {
	env *environment
	col *collection
}

func (s scope) Names() []string {
	var names []string
	for _, vars := range [][]kvtable.Pair{s.envVars(), s.colVars()} {
		for _, v := range enabled(vars) {
			if !slices.Contains(names, v.Key) {
				names = append(names, v.Key)
			}
		}
	}
	return append(names, slices.Sorted(maps.Keys(dynamic))...)
}

func (s scope) Lookup(name string) (value, source string, secret, ok bool) {
	// templatefield looks values up each frame, so a dynamic one is made
	// only by fill.
	if _, ok := dynamic[name]; ok {
		return "发送时生成", "内置", false, true
	}
	if v, ok := lookupVar(s.envVars(), name); ok {
		return v.Value, s.env.Name, v.Kind == kindSecret, true
	}
	if v, ok := lookupVar(s.colVars(), name); ok {
		return v.Value, "集合 " + s.col.Name, v.Kind == kindSecret, true
	}
	return "", "", false, false
}

func (s scope) envVars() []kvtable.Pair {
	if s.env == nil {
		return nil
	}
	return s.env.Vars
}

func (s scope) colVars() []kvtable.Pair {
	if s.col == nil {
		return nil
	}
	return s.col.Vars
}

func lookupVar(vars []kvtable.Pair, name string) (kvtable.Pair, bool) {
	i := slices.IndexFunc(vars, func(v kvtable.Pair) bool { return v.On && v.Key == name })
	if i < 0 {
		return kvtable.Pair{}, false
	}
	return vars[i], true
}

func (s scope) fill(text string) string {
	return variable.ReplaceAllStringFunc(text, func(m string) string {
		name := variable.FindStringSubmatch(m)[1]
		if gen, ok := dynamic[name]; ok {
			return gen()
		}
		if v, _, _, ok := s.Lookup(name); ok {
			return v
		}
		return m
	})
}

func (s scope) fillPairs(pairs []kvtable.Pair) []kvtable.Pair {
	out := slices.Clone(pairs)
	for i := range out {
		out[i].Key, out[i].Value = s.fill(out[i].Key), s.fill(out[i].Value)
	}
	return out
}

// resolve returns a copy of r with the variables s defines filled in.
func (s scope) resolve(r *request) *request {
	cp := *r
	// The URL's query goes as the Params it is synced with, which core
	// escapes once their variables are filled in. A query a variable
	// brings goes as it is.
	cp.URL, cp.Body, cp.BodyFile = s.fill(fillPath(withQuery(r.URL, nil), s.fillPairs(r.PathVars))), s.fill(r.Body), s.fill(r.BodyFile)
	cp.User, cp.Password, cp.Token, cp.Key = s.fill(r.User), s.fill(r.Password), s.fill(r.Token), s.fill(r.Key)
	cp.Params, cp.Headers, cp.Form = s.fillPairs(r.Params), s.fillPairs(r.Headers), s.fillPairs(r.Form)
	return &cp
}

func (s scope) ranges(t *ui.Theme) func(string) []ui.TextRange {
	return func(v string) []ui.TextRange { return templatefield.Ranges(v, s, t) }
}

// set gives the variable name value, turning it on, or adds it.
func (e *environment) set(name, value string) {
	i := slices.IndexFunc(e.Vars, func(v kvtable.Pair) bool { return v.Key == name })
	if i < 0 {
		e.Vars = append(e.Vars, kvtable.Pair{On: true, Key: name, Value: value, Kind: kindText})
		return
	}
	e.Vars[i].On, e.Vars[i].Value = true, value
}

func (a *app) environment(id string) *environment {
	i := slices.IndexFunc(a.ws.Environments, func(e *environment) bool { return e.ID == id })
	if i < 0 {
		return nil
	}
	return a.ws.Environments[i]
}

// env returns the active environment, which requests use.
func (a *app) env() *environment {
	return a.environment(a.ws.Env)
}

// scope is the variables of the request chosen.
func (a *app) scope() scope {
	col, _ := a.ws.find(a.ws.Selected)
	return scope{a.env(), col}
}

// envMenu chooses an environment into *id: the active one, with none among
// them and the dialog editing them, or else the one the dialog edits.
func (a *app) envMenu(c *ui.Context, id *string, active bool) {
	env := a.environment(*id)
	name := "无环境"
	if env != nil {
		name = env.Name
	} else if !active {
		name = "选择环境"
	}
	headerMenu(c, "环境", name).Menu(func(m *ui.Menu) {
		if active && m.Item("无环境").Checked(env == nil).Chosen() {
			*id = ""
		}
		for _, e := range a.ws.Environments {
			if m.Item(e.Name).Checked(e == env).Chosen() {
				*id = e.ID
			}
		}
		if active {
			m.Separator()
			if m.Item("管理环境…").Chosen() {
				a.envOpen, a.editing = true, a.ws.Env
			}
		}
	})
}

func (a *app) newEnvironment() {
	e := &environment{ID: newID(), Name: "新环境"}
	a.ws.Environments = append(a.ws.Environments, e)
	a.editing = e.ID
}

func (a *app) envDialog(c *ui.Context) {
	env := a.environment(a.editing)
	ui.Modal(c, &a.envOpen, func() {
		ui.Column(c).Width(640).Gap(12).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "环境").FontSize(15).Bold()
				ui.Spacer(c)
				if len(a.ws.Environments) > 0 {
					a.envMenu(c, &a.editing, false)
				}
				ui.Button(c, "新建").OnClick(a.newEnvironment)
				if env != nil {
					ui.Button(c, "删除…").OnClick(func() { a.envDeleteOpen = true })
				}
			})
			if env == nil {
				caption(c, "选择或新建一个环境，在请求中以 {{变量}} 引用其中的值")
			} else {
				ui.Field(c, "名称", func() { ui.TextInput(c.Key("env-name-"+env.ID), &env.Name) })
				ui.Scroll(c).MaxHeight(360).Children(func() {
					kvtable.EditorWith(c.Key("vars-"+env.ID), &env.Vars, kvtable.Options{KeyLabel: "变量", ValueLabel: "值", Kinds: varKinds, Reorder: true})
				})
			}
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.PrimaryButton(c, "完成").OnClick(func() { a.envOpen = false })
			})
		})
	})
	if env != nil && ui.AlertDialog(c, &a.envDeleteOpen, fmt.Sprintf("删除环境“%s”？", env.Name),
		"其中的变量会一起删除，无法撤销。", "取消", "删除") == 1 {
		a.ws.Environments = slices.DeleteFunc(a.ws.Environments, func(e *environment) bool { return e == env })
		if a.ws.Env == env.ID {
			a.ws.Env = ""
		}
		a.editing = ""
	}
}
