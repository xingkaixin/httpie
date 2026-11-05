package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

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
			if hist := a.history[r.ID]; len(hist) > 1 {
				iconButton(c, iconHistory, "历史响应").Shrink(0).Menu(func(m *ui.Menu) {
					for _, e := range hist {
						if m.Item(e.label()).Checked(e.is(res)).Chosen() {
							a.showEntry(r.ID, e)
						}
					}
				})
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
