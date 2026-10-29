package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/xingkaixin/mygo-native-ui/ui/kvtable"
	"github.com/xingkaixin/mygo-native-ui/ui/templatefield"
)

func icon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	iconPlus       = icon(`<path d="M5 12h14"/><path d="M12 5v14"/>`)
	iconFolderPlus = icon(`<path d="M12 10v6"/><path d="M9 13h6"/><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>`)
	iconUpDown     = icon(`<path d="m7 15 5 5 5-5"/><path d="m7 9 5-5 5 5"/>`)
	iconCopy       = icon(`<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`)
	iconSend       = icon(`<path d="M14.536 21.686a.5.5 0 0 0 .937-.024l6.5-19a.496.496 0 0 0-.635-.635l-19 6.5a.5.5 0 0 0-.024.937l7.93 3.18a2 2 0 0 1 1.112 1.11z"/><path d="m21.854 2.147-10.94 10.939"/>`)
)

var methodColors = map[string]ui.Color{
	"GET": ui.Hex("#3e9e57"), "POST": ui.Hex("#c08a00"), "PUT": ui.Hex("#2f6fde"),
	"PATCH": ui.Hex("#8b5cf6"), "DELETE": ui.Hex("#d9443a"),
}

func methodColor(t *ui.Theme, m string) ui.Color {
	if c, ok := methodColors[m]; ok {
		return c
	}
	return t.TextMuted
}

func statusColor(code int) ui.Color {
	switch {
	case code >= 500:
		return ui.Hex("#d9443a")
	case code >= 400:
		return ui.Hex("#d97706")
	case code >= 300:
		return ui.Hex("#2f6fde")
	}
	return ui.Hex("#3e9e57")
}

type app struct {
	win       *mygo.Window
	path      string
	resDir    string
	saved     []byte
	ws        *workspace
	responses map[string]*response
	inflight  map[string]context.CancelFunc
	url       ui.Handle
	synced    struct{ id, url string }
	curl      string
	// notice is a toast for the next frame, from a goroutine's Update.
	notice string

	reqTab, resTab int
	bulk           bool
	bodyHL         highlighter

	renaming    *string
	renameTitle string
	renameOpen  bool
	renameTo    string
	deleting    *collection
	deleteOpen  bool
	authOf      *collection
	authOpen    bool

	envOpen, envDeleteOpen bool
	editing                string
}

func (a *app) saveIfChanged() {
	data, err := json.MarshalIndent(a.ws, "", "  ")
	if err != nil || bytes.Equal(data, a.saved) {
		return
	}
	if err := os.WriteFile(a.path, data, 0o644); err != nil {
		log.Printf("save: %v", err)
		return
	}
	a.saved = data
}

func caption(c *ui.Context, s string) ui.Element {
	return ui.Text(c, s).FontSize(12).TextColor(c.Theme().TextMuted)
}

func iconButton(c *ui.Context, svg *ui.SVG, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Padding(5).Radius(6).TextColor(t.TextMuted)
	if b.Hovered() {
		b.Background(t.SurfaceHover).TextColor(t.Text)
	}
	return b.Children(func() { ui.Icon(c, svg).FontSize(14) })
}

// varInput is a TextInput completing {{variables}} as the URL does.
func (a *app) varInput(c *ui.Context, text *string) ui.Element {
	t := c.Theme()
	in := templatefield.Input(c, text, a.env()).Padding(t.Space(1.5), t.Space(2.5)).Radius(t.Radius).Background(t.Surface).Border(1, t.Border)
	switch {
	case in.Focused():
		in.BorderColor(t.Accent)
	case in.Hovered():
		in.BorderColor(t.Border.Mix(t.Text, 0.25))
	}
	return in
}

// headerMenu is a drop-down of the header showing text, whose menu the
// caller adds.
func headerMenu(c *ui.Context, label, text string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Gap(6).Padding(6, 10).Radius(6).Border(1, t.Border)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	return b.Children(func() {
		ui.Text(c, text).FontSize(13)
		ui.Icon(c, iconUpDown).FontSize(10).TextColor(t.TextMuted)
	})
}

// menuSelect is a compact drop-down, as HTTPie's "JSON ⇕", showing a menu of the system's.
func menuSelect(c *ui.Context, label string, value *string, options []string) {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Gap(4).Padding(4, 8).Radius(6).Border(1, t.Border)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Text(c, *value).FontSize(12)
		ui.Icon(c, iconUpDown).FontSize(10).TextColor(t.TextMuted)
	})
	b.Menu(func(m *ui.Menu) {
		for _, o := range options {
			if m.Item(o).Checked(o == *value).Chosen() {
				*value = o
			}
		}
	})
}

var formKinds = []kvtable.Kind{{Name: kindText}, {Name: kindFile, Cell: kvtable.File}}

var headerNames = []string{
	"Accept", "Accept-Encoding", "Accept-Language", "Authorization", "Cache-Control", "Content-Type",
	"Cookie", "If-Modified-Since", "If-None-Match", "Origin", "Referer", "User-Agent", "X-Requested-With",
}

var mediaTypes = []string{"application/json", "application/x-www-form-urlencoded", "multipart/form-data", "text/plain", "text/html", "application/xml", "*/*"}

func headerValues(key string) []string {
	switch strings.ToLower(key) {
	case "accept", "content-type":
		return mediaTypes
	case "accept-encoding":
		return []string{"gzip, deflate, br", "identity"}
	case "cache-control":
		return []string{"no-cache", "no-store", "max-age=0"}
	case "authorization":
		return []string{"Bearer ", "Basic "}
	}
	return nil
}

func withCount(label string, rows []kvtable.Pair) string {
	if n := len(enabled(rows)); n > 0 {
		return fmt.Sprintf("%s %d", label, n)
	}
	return label
}

func (a *app) view(c *ui.Context) {
	t := *c.Theme()
	t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#3e9e57"), ui.Hex("#368c4c"), ui.Hex("#2f7a42")
	c.SetTheme(&t)
	if c.Shortcut(ui.Cmd, ui.KeyEnter) {
		a.send()
	}
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		a.newRequest(nil)
	}
	if _, r := a.ws.find(a.ws.Selected); r != nil && c.Shortcut(ui.Cmd, ui.KeyL) {
		a.url.Focus()
	}
	if a.notice != "" {
		c.Toast(a.notice)
		a.notice = ""
	}

	c.Root().Background(ui.Transparent)
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		a.sidebar(c)
		ui.Column(c).Grow(1).Basis(0).Background(t.Background).Children(func() {
			col, r := a.ws.find(a.ws.Selected)
			if r == nil {
				ui.Column(c).Fill().Center().Gap(12).DragWindow().Children(func() {
					caption(c, "选择或新建一个请求")
					ui.PrimaryButton(c, "新建请求").OnClick(func() { a.newRequest(nil) })
				})
				return
			}
			a.header(c, col, r)
			a.urlBar(c, r)
			ui.Split(c, &a.ws.Split, func() { a.requestPane(c, col, r) }, func() { a.responsePane(c, r) }).
				Grow(1).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
		})
	})
	a.dialogs(c)
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	var action func()
	ui.Column(c).Width(250).Padding(max(c.TitleBar().Height, 12), 0, 0, 0).Children(func() {
		ui.Row(c).Padding(0, 10, 4, 18).Gap(2).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Spacer(c)
			iconButton(c, iconPlus, "新建请求 ⌘N").OnClick(func() { a.newRequest(nil) })
			iconButton(c, iconFolderPlus, "新建集合").OnClick(a.newCollection)
		})
		ui.Sidebar(c, &a.ws.Selected, func() {
			for _, col := range a.ws.Collections {
				ui.SidebarSection(c, col.Name, &col.Open, func() {
					for _, r := range col.Requests {
						// The item's label grows; Reverse puts the method tag, built after it, on its left.
						ui.SidebarItem(c, r.ID, nil, r.Name).Reverse().Children(func() {
							tag := ui.Text(c, methodShort(r.Method)).Font("monospace").FontSize(9).Bold().Width(32).Shrink(0)
							if r.ID != a.ws.Selected {
								tag.TextColor(methodColor(t, r.Method))
							}
						}).ContextMenu(func(m *ui.Menu) {
							if m.Item("重命名…").Chosen() {
								action = func() { a.rename(&r.Name, "重命名请求") }
							}
							if m.Item("复制请求").Chosen() {
								action = func() { a.duplicate(col, r) }
							}
							m.Submenu("移动到", func(sub *ui.Menu) {
								for _, to := range a.ws.Collections {
									if sub.Item(to.Name).Disabled(to == col).Chosen() {
										action = func() { a.moveRequest(col, r, to) }
									}
								}
							}).Disabled(len(a.ws.Collections) == 1)
							m.Separator()
							if m.Item("删除").Chosen() {
								action = func() { a.deleteRequest(c, col, r) }
							}
						})
					}
				}).ContextMenu(func(m *ui.Menu) {
					if m.Item("新建请求").Chosen() {
						action = func() { a.newRequest(col) }
					}
					if m.Item("重命名…").Chosen() {
						action = func() { a.rename(&col.Name, "重命名集合") }
					}
					if m.Item("集合认证…").Chosen() {
						action = func() { a.authOf, a.authOpen = col, true }
					}
					m.Separator()
					if m.Item("删除集合…").Disabled(len(a.ws.Collections) == 1).Chosen() {
						action = func() { a.deleting, a.deleteOpen = col, true }
					}
				})
			}
		}).Grow(1).Background(ui.Transparent).Label("Collections")
	})
	if action != nil {
		action()
	}
}

func methodShort(m string) string {
	switch m {
	case "DELETE":
		return "DEL"
	case "OPTIONS":
		return "OPT"
	}
	return m
}

func (a *app) header(c *ui.Context, col *collection, r *request) {
	ui.Row(c).Height(max(c.TitleBar().Height, 44)).Padding(0, 16).Gap(6).AlignItems(ui.Center).DragWindow().Children(func() {
		caption(c, col.Name+"  /").FontSize(13)
		ui.EditableText(c.Key("name-"+r.ID), &r.Name).FontSize(13).FontWeight(600).Tooltip("双击重命名").OnChange(func() {
			if r.Name == "" {
				r.Name = "Untitled"
			}
		})
		ui.Spacer(c)
		if len(a.ws.Cookies) > 0 {
			a.cookieMenu(c)
		}
		a.envMenu(c, &a.ws.Env, true)
		if ui.Button(c, "复制命令").Tooltip("复制为 http 命令").Clicked() {
			c.WriteClipboard(command(requestArgs(a.env().resolve(col.effective(r)))))
			c.Toast("已复制 http 命令")
		}
	})
}

// cookieMenu lists the cookies the requests send, as the responses set
// them, and clears them.
func (a *app) cookieMenu(c *ui.Context) {
	headerMenu(c, "Cookie", fmt.Sprintf("Cookie %d", len(a.ws.Cookies))).Menu(func(m *ui.Menu) {
		for _, ck := range a.ws.Cookies {
			// core keeps a dotless host's cookies, as localhost's, under
			// host.local, as Python's cookiejar does.
			m.Item(ck.Name + " · " + strings.TrimSuffix(*ck.Domain, ".local")).Disabled(true)
		}
		m.Separator()
		if m.Item("清空 Cookie").Chosen() {
			a.ws.Cookies = nil
		}
	})
}

func (a *app) urlBar(c *ui.Context, r *request) {
	t := c.Theme()
	if a.curl != "" {
		a.importCurl(c, r, a.curl)
		a.curl = ""
	}
	_, sending := a.inflight[r.ID]
	ui.Row(c).Margin(0, 16, 12, 16).Padding(4, 4, 4, 6).Gap(4).Radius(10).Border(1, t.Border).Background(t.Surface).AlignItems(ui.Center).Children(func() {
		m := ui.ButtonBase(c).Label("请求方法").Gap(4).Padding(6, 8).Radius(6)
		if m.Hovered() {
			m.Background(t.SurfaceHover)
		}
		m.Children(func() {
			ui.Text(c, r.Method).Font("monospace").FontWeight(700).TextColor(methodColor(t, r.Method))
			ui.Icon(c, iconUpDown).FontSize(10).TextColor(t.TextMuted)
		})
		m.Menu(func(mm *ui.Menu) {
			for _, meth := range methods {
				if mm.Item(meth).Checked(meth == r.Method).Chosen() {
					r.Method = meth
				}
			}
		})
		templatefield.Input(c.Key("url-"+r.ID), &r.URL, a.env()).Bind(&a.url).Placeholder("httpbin.org/get，或 :3000/api").Label("URL").
			Font("monospace").Grow(1).Padding(6, 4).OnSubmit(a.send).OnPaste(a.pasteCurl)
		if sending {
			if ui.Button(c, "取消").Clicked() {
				a.inflight[r.ID]()
				c.Toast("已取消")
			}
		} else {
			ui.PrimaryButton(c, "").Label("Send").Tooltip("发送 ⌘↩").OnClick(a.send).Children(func() {
				ui.Text(c, "Send").SingleLine()
				ui.Icon(c, iconSend).FontSize(12)
			})
		}
	})
	// Only an edit of the URL is parsed: parsing on a switch of requests
	// would move the parameters that are off to the end.
	if a.synced.id == r.ID && a.synced.url != r.URL {
		r.Params = parse(r.URL, r.Params)
	}
	a.synced.id, a.synced.url = r.ID, r.URL
}

func (a *app) requestPane(c *ui.Context, col *collection, r *request) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(10, 16).Children(func() {
			body, auth := "Body", "Auth"
			if r.BodyType != bodyNone {
				body += " · " + r.BodyType
			}
			if r.AuthType != authNone {
				auth += " · " + r.AuthType
			}
			ui.Segmented(c, &a.reqTab, withCount("Params", r.Params), withCount("Headers", r.Headers), body, auth, "Settings").Label("请求内容")
		})
		switch a.reqTab {
		case 0, 1:
			ui.Scroll(c).Grow(1).Padding(0, 16, 16, 16).Gap(6).Children(func() {
				ui.Row(c).Justify(ui.End).Children(func() { a.bulkButton(c) })
				if a.reqTab == 0 {
					if kvtable.EditorWith(c.Key("params-"+r.ID), &r.Params, kvtable.Options{KeyLabel: "参数", ValueLabel: "值", Ranges: a.ranges(c), Reorder: true, Bulk: &a.bulk}).Changed() {
						r.URL = withQuery(r.URL, r.Params)
						a.synced.url = r.URL
					}
				} else {
					kvtable.EditorWith(c.Key("headers-"+r.ID), &r.Headers, kvtable.Options{
						KeyLabel: "Header", ValueLabel: "值", Keys: headerNames, Values: headerValues, Ranges: a.ranges(c), Reorder: true, Bulk: &a.bulk,
					})
				}
			})
		case 2:
			a.bodyEditor(c, r)
		case 3:
			a.authEditor(c, col, r)
		case 4:
			settingsEditor(c, r)
		}
	})
}

func (a *app) bodyEditor(c *ui.Context, r *request) {
	t := c.Theme()
	ui.Column(c).Grow(1).Padding(0, 16, 16, 16).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			menuSelect(c, "Body 类型", &r.BodyType, bodyTypes)
			ui.Spacer(c)
			if r.BodyType == bodyJSON {
				// Variables may stand for values, as in {"id": {{id}}}, so the body is
				// checked as sent; formatting works on it as written.
				valid := json.Valid([]byte(r.Body))
				if !valid && r.Body != "" && !json.Valid([]byte(a.env().fill(r.Body))) {
					ui.Text(c, "JSON 格式有误").FontSize(12).TextColor(t.Danger)
				}
				ui.Button(c, "格式化").Disabled(!valid).OnClick(func() {
					var out bytes.Buffer
					if json.Indent(&out, []byte(r.Body), "", "  ") == nil {
						r.Body = out.String()
					}
				})
			}
		})
		switch r.BodyType {
		case bodyNone:
			ui.Column(c).Grow(1).Center().Children(func() { caption(c, "此请求不带 Body") })
		case bodyForm, bodyMultipart:
			ui.Scroll(c).Grow(1).Children(func() {
				kvtable.EditorWith(c.Key("form-"+r.ID), &r.Form, kvtable.Options{
					KeyLabel: "字段", ValueLabel: "值", Kinds: formKinds, Choose: a.chooseFile, Ranges: a.ranges(c), Reorder: true,
				})
			})
		case bodyFile:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				name := r.BodyFile
				if name == "" {
					name = "未选择文件"
				}
				ui.Text(c, name).Font("monospace").FontSize(12).Grow(1).TextColor(t.TextMuted)
				ui.Button(c, "选择文件…").OnClick(func() { a.chooseFile(func(path string) { r.BodyFile = path }) })
			})
		default:
			ed := ui.TextAreaBase(c.Key("body-"+r.ID), &r.Body).Label("Body").Font("monospace").FontSize(12.5).
				Grow(1).Padding(10).Radius(8).Border(1, t.Border).Background(t.Surface)
			if r.BodyType == bodyJSON {
				ed.Placeholder("{\n  \"key\": \"value\"\n}").TextRanges(a.bodyHL.ranges(t, r.Body)...)
			}
			ed.TextRanges(templatefield.Ranges(r.Body, a.env(), t)...)
		}
	})
}

func (a *app) authEditor(c *ui.Context, col *collection, r *request) {
	ui.Column(c).Grow(1).Padding(0, 16, 16, 16).Gap(12).Children(func() {
		a.authFields(c, &r.auth, requestAuthTypes)
		switch r.AuthType {
		case authNone:
			caption(c, "此请求不带认证信息")
		case authInherit:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				caption(c, "继承自集合："+col.Auth.AuthType)
				ui.Button(c, "编辑").OnClick(func() { a.authOf, a.authOpen = col, true })
			})
		}
	})
}

// authFields edits au, of one of types.
func (a *app) authFields(c *ui.Context, au *auth, types []string) {
	ui.Row(c).Children(func() { menuSelect(c, "认证方式", &au.AuthType, types) })
	switch au.AuthType {
	case authBasic, authDigest:
		ui.Field(c, "用户名", func() { a.varInput(c, &au.User) })
		ui.Field(c, "密码", func() { a.varInput(c, &au.Password).Password() })
	case authBearer:
		ui.Field(c, "Token", func() { a.varInput(c, &au.Token).Font("monospace") })
	}
}

func (a *app) bulkButton(c *ui.Context) {
	t := c.Theme()
	label := "批量编辑"
	if a.bulk {
		label = "表格编辑"
	}
	b := ui.ButtonBase(c).Label(label).Padding(2, 6).Radius(4).TextColor(t.TextMuted)
	if b.Hovered() {
		b.Background(t.SurfaceHover).TextColor(t.Text)
	}
	b.OnClick(func() { a.bulk = !a.bulk }).Children(func() { ui.Text(c, label).FontSize(12) })
}

func settingsEditor(c *ui.Context, r *request) {
	ui.Scroll(c).Grow(1).Padding(0, 16, 16, 16).Gap(12).Children(func() {
		ui.Checkbox(c, &r.Follow, "跟随重定向")
		verify := !r.Insecure
		if ui.Checkbox(c, &verify, "校验 TLS 证书").Changed() {
			r.Insecure = !verify
		}
		ui.Field(c, "超时（秒，0 为不限）", func() { ui.NumberInput(c, &r.Timeout, 0, 3600, 1).Width(120) })
		ui.Field(c, "2xx JSON 响应写入环境变量", func() {
			kvtable.EditorWith(c.Key("extract-"+r.ID), &r.Extract, kvtable.Options{KeyLabel: "变量", ValueLabel: "JSON 路径，如 $.data.token"})
		})
	})
}

func (a *app) responsePane(c *ui.Context, r *request) {
	t := c.Theme()
	res, loaded := a.responses[r.ID]
	if !loaded {
		a.loadResponse(r.ID)
	}
	_, sending := a.inflight[r.ID]
	waiting := sending && (res == nil || !res.streaming)
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(10, 16).Gap(10).AlignItems(ui.Center).Children(func() {
			headers := "Headers"
			if res != nil && len(res.Headers) > 0 {
				headers = fmt.Sprintf("Headers %d", len(res.Headers))
			}
			ui.Segmented(c, &a.resTab, "Body", headers, "Request").Label("响应内容")
			ui.Spacer(c)
			if res == nil || res.Code == 0 || waiting {
				return
			}
			color := statusColor(res.Code)
			ui.Row(c).Padding(3, 8).Radius(6).Background(color.Alpha(0.14)).Children(func() {
				ui.Text(c, res.Status).FontSize(12).FontWeight(600).TextColor(color).SingleLine()
			})
			caption(c, res.Duration.Round(time.Millisecond).String()).FontFeatures("tnum").SingleLine().Shrink(0)
			caption(c, formatSize(res.Size)).SingleLine().Shrink(0)
			if at := ago(res.At); at != "" {
				caption(c, at).Tooltip(res.At.Format("2006-01-02 15:04:05")).SingleLine().Shrink(0)
			}
			if iconButton(c, iconCopy, "复制响应").Shrink(0).Clicked() {
				c.WriteClipboard(res.view.Buffer.String())
				c.Toast("已复制响应内容")
			}
		})
		switch {
		case waiting:
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Spinner(c)
				caption(c, "请求中…")
			})
		case res == nil:
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Box(c).Size(56, 56).Radius(28).Background(t.Surface).Center().Children(func() {
					ui.Icon(c, iconSend).FontSize(22).TextColor(t.TextMuted)
				})
				ui.Text(c, "Not sent").TextColor(t.TextMuted)
				caption(c, "⌘↩ 发送请求")
			})
		case res.Code == 0:
			ui.Column(c).Grow(1).Padding(0, 16, 16, 16).Children(func() {
				ui.Text(c, res.Err).TextColor(t.Danger).Selectable()
			})
		case a.resTab == 1:
			ui.Scroll(c).Grow(1).Padding(0, 16, 16, 16).Gap(6).Selectable().Children(func() {
				if len(res.Redirects) > 0 {
					caption(c, "重定向")
					for _, h := range res.Redirects {
						ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
							ui.Text(c, h.Status).FontSize(12).FontWeight(600).TextColor(statusColor(h.Code)).Width(180).Shrink(0)
							ui.Text(c, h.Location).Font("monospace").FontSize(12).TextColor(t.TextMuted).Grow(1)
						})
					}
					ui.Divider(c).Margin(6, 0)
				}
				for _, h := range res.Headers {
					ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
						ui.Text(c, h.Name).Font("monospace").FontSize(12).FontWeight(600).Width(180).Shrink(0)
						ui.Text(c, h.Value).Font("monospace").FontSize(12).TextColor(t.TextMuted).Grow(1)
					})
				}
			})
		case a.resTab == 2:
			editor.View(c.Key("sent-"+r.ID), res.sentView).Grow(1).Margin(0, 16, 16, 16)
		default:
			if res.Err != "" {
				caption(c, res.Err).Padding(0, 16, 6, 16)
			}
			if res.Truncated {
				caption(c, "响应超过 10 MB，只显示前 10 MB").Padding(0, 16, 6, 16)
			}
			editor.View(c.Key("res-"+r.ID), res.view).Grow(1).Margin(0, 16, 16, 16)
		}
	})
}

// buildViews makes the editors showing res, which take long for a large
// body, off the main thread.
func (res *response) buildViews() {
	res.view = textView(res.Body, "响应 Body", res.JSON)
	res.sentView = textView(res.Sent, "实际发出的请求", false)
}

// appendText adds text to the end of st, which follows it there unless
// the end is out of view or the caret is elsewhere.
func appendText(st *editor.State, text string) {
	if text == "" {
		return
	}
	b := st.Buffer
	n := b.Len()
	_, last := st.VisibleLines()
	sel := st.Selection()
	follow := sel.Anchor == n && sel.Head == n && last >= b.LineCount()-1
	b.Edit(editor.Edit{Start: n, End: n, Text: text})
	if follow {
		st.Select(b.Len(), b.Len())
	}
}

// textView returns a read-only editor of text.
func textView(text, label string, isJSON bool) *editor.State {
	buf := editor.NewBuffer(text)
	if isJSON {
		buf.SetLanguage(editor.LanguageFor("body.json"))
	}
	return &editor.State{
		Buffer:          buf,
		Font:            ui.Font{Size: 12.5},
		ReadOnly:        true,
		SoftWrap:        !isJSON,
		NoActiveLine:    true,
		NoScrollPastEnd: true,
		Label:           label,
	}
}

func (a *app) send() {
	col, r := a.ws.find(a.ws.Selected)
	if r == nil {
		return
	}
	if _, busy := a.inflight[r.ID]; busy {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.inflight[r.ID] = cancel
	req := a.env().resolve(col.effective(r))
	id, args, jar := r.ID, requestArgs(req), a.ws.Cookies
	// Variables go to the environment the request was sent with.
	rules, envID := enabled(r.Extract), a.ws.Env
	var live *response
	progress := func(p response, text string) {
		a.win.Update(func() {
			if live == nil {
				live = &p
				live.streaming = true
				a.responses[id] = live
			}
			live.Size, live.Duration = p.Size, p.Duration
			appendText(live.view, text)
		})
	}
	go func() {
		res := send(ctx, append(systemProxy(req.URL), args...), jar, progress)
		cancel()
		if res == nil {
			// A canceled request keeps the response it had.
			a.win.Update(func() { delete(a.inflight, id) })
			return
		}
		// A body streamed has the views that showed it.
		if res.view == nil {
			res.buildViews()
		}
		if res.Code != 0 {
			saveResponse(a.resDir, id, res)
		}
		extracting := len(rules) > 0 && res.Code/100 == 2 && res.JSON && !res.Truncated
		var vars []kvtable.Pair
		var missing []string
		if extracting {
			vars, missing = extract(res.Body, rules)
		}
		a.win.Update(func() {
			delete(a.inflight, id)
			a.responses[id] = res
			if res.Code != 0 {
				a.ws.Cookies = res.Cookies
			}
			if extracting {
				a.setVars(envID, vars, missing)
			}
		})
	}()
}

// setVars sets vars in the environment envID, telling of the paths a
// response was missing.
func (a *app) setVars(envID string, vars []kvtable.Pair, missing []string) {
	env := a.environment(envID)
	if env == nil {
		a.notice = "未选择环境，提取的变量没有写入"
		return
	}
	for _, v := range vars {
		env.set(v.Key, v.Value)
	}
	if len(missing) > 0 {
		a.notice = "响应中没有 " + strings.Join(missing, "、")
	}
}

// loadResponse shows the response the request id had when the app last
// ran, unless a send's comes first.
func (a *app) loadResponse(id string) {
	a.responses[id] = nil
	go func() {
		res := readResponse(a.resDir, id)
		if res == nil {
			return
		}
		res.buildViews()
		a.win.Update(func() {
			if a.responses[id] == nil {
				a.responses[id] = res
			}
		})
	}()
}

// chooseFile shows a dialog choosing a file, and calls set with its path.
func (a *app) chooseFile(set func(path string)) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "选择文件"})
		if err != nil || len(paths) == 0 {
			return
		}
		a.win.Update(func() { set(paths[0]) })
	}()
}

func (a *app) newRequest(col *collection) {
	if col == nil {
		col, _ = a.ws.find(a.ws.Selected)
	}
	if col == nil {
		col = a.ws.Collections[0]
	}
	r := newRequest("Untitled", "GET", "")
	col.Requests = append(col.Requests, r)
	col.Open = true
	a.ws.Selected, a.reqTab = r.ID, 0
	a.url.Focus()
}

func (a *app) newCollection() {
	col := &collection{ID: newID(), Name: "新集合", Open: true, Auth: auth{AuthType: authNone}}
	a.ws.Collections = append(a.ws.Collections, col)
	a.rename(&col.Name, "重命名集合")
}

func (a *app) rename(name *string, title string) {
	a.renaming, a.renameTitle, a.renameTo, a.renameOpen = name, title, *name, true
}

func (a *app) moveRequest(from *collection, r *request, to *collection) {
	from.Requests = slices.DeleteFunc(from.Requests, func(x *request) bool { return x == r })
	to.Requests, to.Open = append(to.Requests, r), true
}

func (a *app) duplicate(col *collection, r *request) {
	cp := *r
	cp.ID, cp.Name = newID(), r.Name+" 副本"
	cp.Params, cp.Headers, cp.Form, cp.Extract = slices.Clone(r.Params), slices.Clone(r.Headers), slices.Clone(r.Form), slices.Clone(r.Extract)
	i := slices.Index(col.Requests, r)
	col.Requests = slices.Insert(col.Requests, i+1, &cp)
	a.ws.Selected = cp.ID
}

// pasteCurl takes a curl command pasted into the URL for the next build to
// import: the input would put back its own text over a URL set now,
// between frames.
func (a *app) pasteCurl(text string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "curl ") {
		return false
	}
	a.curl = text
	return true
}

// importCurl fills r with the request of a curl command, keeping its name.
func (a *app) importCurl(c *ui.Context, r *request, cmd string) {
	imported, err := parseCurl(cmd)
	if err != nil {
		c.Toast("无法导入 cURL：" + err.Error())
		return
	}
	old := *r
	imported.ID, imported.Name = r.ID, r.Name
	*r = *imported
	c.ToastAction("已从 cURL 导入", "撤销", func() {
		*r = old
		// The URL comes back with its Params, not as an edit to parse.
		a.synced.id, a.synced.url = r.ID, r.URL
	})
}

func (a *app) deleteRequest(c *ui.Context, col *collection, r *request) {
	i := slices.Index(col.Requests, r)
	col.Requests = slices.Delete(col.Requests, i, i+1)
	if a.ws.Selected == r.ID {
		a.ws.Selected = ""
		if len(col.Requests) > 0 {
			a.ws.Selected = col.Requests[min(i, len(col.Requests)-1)].ID
		}
	}
	c.ToastAction("已删除 "+r.Name, "撤销", func() {
		col.Requests = slices.Insert(col.Requests, min(i, len(col.Requests)), r)
		a.ws.Selected = r.ID
	})
}

func (a *app) dialogs(c *ui.Context) {
	a.envDialog(c)
	ui.Modal(c, &a.renameOpen, func() {
		rename := func() {
			if a.renameTo != "" {
				*a.renaming = a.renameTo
			}
			a.renameOpen = false
		}
		ui.Column(c).Width(320).Gap(12).Children(func() {
			ui.Text(c, a.renameTitle).FontSize(15).Bold()
			ui.TextInput(c, &a.renameTo).AutoFocus().OnSubmit(rename)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, "取消").OnClick(func() { a.renameOpen = false })
				ui.PrimaryButton(c, "保存").OnClick(rename)
			})
		})
	})
	ui.Modal(c, &a.authOpen, func() {
		ui.Column(c).Width(360).Gap(12).Children(func() {
			ui.Text(c, fmt.Sprintf("集合“%s”的认证", a.authOf.Name)).FontSize(15).Bold()
			a.authFields(c, &a.authOf.Auth, authTypes)
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.PrimaryButton(c, "完成").OnClick(func() { a.authOpen = false })
			})
		})
	})
	if a.deleting != nil && ui.AlertDialog(c, &a.deleteOpen, fmt.Sprintf("删除集合“%s”？", a.deleting.Name),
		fmt.Sprintf("其中的 %d 个请求会一起删除，无法撤销。", len(a.deleting.Requests)), "取消", "删除") == 1 {
		a.ws.Collections = slices.DeleteFunc(a.ws.Collections, func(col *collection) bool { return col == a.deleting })
		if col, _ := a.ws.find(a.ws.Selected); col == nil {
			a.ws.Selected = ""
		}
		a.deleting = nil
	}
}

func main() {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	path := filepath.Join(dir, "workspace.json")
	ws, err := loadWorkspace(path)
	if err != nil {
		log.Fatal(err)
	}
	resDir := filepath.Join(dir, "responses")
	pruneResponses(resDir, ws)
	a := &app{
		path: path, resDir: resDir, ws: ws,
		responses: map[string]*response{},
		inflight:  map[string]context.CancelFunc{},
	}
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:         "HTTP Desk",
			Width:         1200,
			Height:        780,
			MinWidth:      900,
			MinHeight:     560,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHiddenInset,
			Vibrancy:      mygo.VibrancySidebar,
			Content:       ui.View(a.view),
		})
		go func() {
			for range time.Tick(time.Second) {
				mygo.RunOnMain(a.saveIfChanged)
			}
		}()
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.saveIfChanged() })
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
