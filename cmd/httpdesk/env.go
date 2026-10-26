package main

import (
	"regexp"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
	"github.com/xingkaixin/mygo-native-ui/ui/templatefield"
)

const kindSecret = "Secret"

var varKinds = []kvtable.Kind{{Name: kindText}, {Name: kindSecret, Cell: kvtable.Secret}}

// variable matches a variable as templatefield writes one.
var variable = regexp.MustCompile(`\{\{ *([\w.-]+) *\}\}`)

type environment struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Vars []kvtable.Pair `json:"vars,omitempty"`
}

// Names, Lookup and fill take a nil environment, as no environment chosen,
// which defines nothing.
func (e *environment) Names() []string {
	if e == nil {
		return nil
	}
	var names []string
	for _, v := range enabled(e.Vars) {
		names = append(names, v.Key)
	}
	return names
}

func (e *environment) Lookup(name string) (value, source string, secret, ok bool) {
	if e == nil {
		return "", "", false, false
	}
	vars := enabled(e.Vars)
	if i := slices.IndexFunc(vars, func(v kvtable.Pair) bool { return v.Key == name }); i >= 0 {
		return vars[i].Value, e.Name, vars[i].Kind == kindSecret, true
	}
	return "", "", false, false
}

func (e *environment) fill(s string) string {
	return variable.ReplaceAllStringFunc(s, func(m string) string {
		if v, _, _, ok := e.Lookup(variable.FindStringSubmatch(m)[1]); ok {
			return v
		}
		return m
	})
}

func (e *environment) fillPairs(pairs []kvtable.Pair) []kvtable.Pair {
	out := slices.Clone(pairs)
	for i := range out {
		out[i].Key, out[i].Value = e.fill(out[i].Key), e.fill(out[i].Value)
	}
	return out
}

// resolve returns a copy of r with the variables e defines filled in.
func (e *environment) resolve(r *request) *request {
	cp := *r
	cp.URL, cp.Body, cp.BodyFile = e.fill(r.URL), e.fill(r.Body), e.fill(r.BodyFile)
	cp.User, cp.Password, cp.Token = e.fill(r.User), e.fill(r.Password), e.fill(r.Token)
	cp.Params, cp.Headers, cp.Form = e.fillPairs(r.Params), e.fillPairs(r.Headers), e.fillPairs(r.Form)
	return &cp
}

func (a *app) env() *environment {
	i := slices.IndexFunc(a.ws.Environments, func(e *environment) bool { return e.ID == a.ws.Env })
	if i < 0 {
		return nil
	}
	return a.ws.Environments[i]
}

func (a *app) ranges(c *ui.Context) func(string) []ui.TextRange {
	env, t := a.env(), c.Theme()
	return func(v string) []ui.TextRange { return templatefield.Ranges(v, env, t) }
}

// envMenu chooses the environment, and opens the dialog editing them
// unless it is in it.
func (a *app) envMenu(c *ui.Context, manage bool) {
	t := c.Theme()
	env := a.env()
	name := "无环境"
	if env != nil {
		name = env.Name
	}
	b := ui.ButtonBase(c).Label("环境").Gap(6).Padding(6, 10).Radius(6).Border(1, t.Border)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Text(c, name).FontSize(13)
		ui.Icon(c, iconUpDown).FontSize(10).TextColor(t.TextMuted)
	})
	b.Menu(func(m *ui.Menu) {
		if m.Item("无环境").Checked(env == nil).Chosen() {
			a.ws.Env = ""
		}
		for _, e := range a.ws.Environments {
			if m.Item(e.Name).Checked(e == env).Chosen() {
				a.ws.Env = e.ID
			}
		}
		if manage {
			m.Separator()
			if m.Item("管理环境…").Chosen() {
				a.envOpen = true
			}
		}
	})
}

func (a *app) newEnvironment() {
	e := &environment{ID: newID(), Name: "新环境"}
	a.ws.Environments = append(a.ws.Environments, e)
	a.ws.Env = e.ID
}

func (a *app) envDialog(c *ui.Context) {
	ui.Modal(c, &a.envOpen, func() {
		env := a.env()
		ui.Column(c).Width(640).Gap(12).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "环境").FontSize(15).Bold()
				ui.Spacer(c)
				a.envMenu(c, false)
				ui.Button(c, "新建").OnClick(a.newEnvironment)
				if env != nil {
					ui.Button(c, "删除").OnClick(func() {
						a.ws.Environments = slices.DeleteFunc(a.ws.Environments, func(e *environment) bool { return e == env })
						a.ws.Env = ""
					})
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
}
