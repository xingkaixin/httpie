package main

import (
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	proxySystem = "系统代理"
	proxyNone   = "直连"
	proxyCustom = "自定义"
)

var proxyModes = []string{proxySystem, proxyNone, proxyCustom}

// network is how every request connects: its proxy, the CAs it trusts
// besides the system's, and the certificates it shows hosts.
type network struct {
	ProxyMode string       `json:"proxyMode,omitempty"`
	ProxyURL  string       `json:"proxyURL,omitempty"`
	CA        string       `json:"ca,omitempty"`
	Certs     []clientCert `json:"certs,omitempty"`
}

// clientCert is shown to Host, a name or a name and port. Key is empty
// when Cert holds the key too.
type clientCert struct {
	Host string `json:"host"`
	Cert string `json:"cert"`
	Key  string `json:"key,omitempty"`
}

// completeURL parses raw as core completes it: ":3000/a" is localhost's,
// and a URL without a scheme is http.
func completeURL(raw string) (*url.URL, error) {
	raw = strings.TrimPrefix(raw, "://")
	if !hasScheme.MatchString(raw) {
		if strings.HasPrefix(raw, ":") {
			raw = "localhost" + raw
		}
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

// args returns the options connecting to rawURL as n says, but for the
// system's proxy, which only the app knows: a copied command goes through
// the proxy of its shell.
func (n *network) args(rawURL string) []string {
	var opts []string
	if n.ProxyMode == proxyCustom && n.ProxyURL != "" {
		opts = append(opts, "--proxy=http:"+n.ProxyURL, "--proxy=https:"+n.ProxyURL)
	}
	if n.CA != "" {
		opts = append(opts, "--verify="+n.CA)
	}
	if c := n.cert(rawURL); c != nil {
		opts = append(opts, "--cert="+c.Cert)
		if c.Key != "" {
			opts = append(opts, "--cert-key="+c.Key)
		}
	}
	return opts
}

func (n *network) cert(rawURL string) *clientCert {
	u, err := completeURL(rawURL)
	if err != nil {
		return nil
	}
	i := slices.IndexFunc(n.Certs, func(c clientCert) bool {
		return c.Cert != "" && (strings.EqualFold(c.Host, u.Hostname()) || strings.EqualFold(c.Host, u.Host))
	})
	if i < 0 {
		return nil
	}
	return &n.Certs[i]
}

func (a *app) networkDialog(c *ui.Context) {
	n := &a.ws.Network
	if n.ProxyMode == "" {
		n.ProxyMode = proxySystem
	}
	ui.Modal(c, &a.networkOpen, func() {
		ui.Column(c).Width(560).Gap(12).Children(func() {
			ui.Text(c, "网络设置").FontSize(15).Bold()
			ui.Field(c, "代理", func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					menuSelect(c, "代理", &n.ProxyMode, proxyModes)
					if n.ProxyMode == proxyCustom {
						ui.TextInput(c, &n.ProxyURL).Placeholder("http://127.0.0.1:7890 或 socks5://…").Font("monospace").Grow(1)
					}
				})
			})
			ui.Field(c, "CA 证书", func() {
				a.fileRow(c, &n.CA, "系统信任库")
			})
			ui.Field(c, "客户端证书", func() {
				ui.Column(c).Gap(6).Children(func() {
					for i := range n.Certs {
						cert := &n.Certs[i]
						ui.Row(c.Key(i)).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.TextInput(c, &cert.Host).Placeholder("api.example.com").Font("monospace").Width(170)
							a.fileButton(c, &cert.Cert, "证书")
							a.fileButton(c, &cert.Key, "私钥")
							ui.Spacer(c)
							if ui.Button(c, "移除").Clicked() {
								n.Certs = slices.Delete(n.Certs, i, i+1)
							}
						})
					}
					ui.Row(c).Children(func() {
						if ui.Button(c, "添加证书").Clicked() {
							n.Certs = append(n.Certs, clientCert{})
						}
					})
				})
			})
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.PrimaryButton(c, "完成").OnClick(func() { a.networkOpen = false })
			})
		})
	})
}

// fileRow shows the file *path names, with buttons choosing and clearing
// it, or empty when there is none.
func (a *app) fileRow(c *ui.Context, path *string, empty string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		name := *path
		if name == "" {
			name = empty
		}
		ui.Text(c, name).Font("monospace").FontSize(12).Grow(1).TextColor(c.Theme().TextMuted)
		ui.Button(c, "选择文件…").OnClick(func() { a.chooseFile(func(p string) { *path = p }) })
		if *path != "" {
			ui.Button(c, "清除").OnClick(func() { *path = "" })
		}
	})
}

// fileButton chooses the file *path names, which it shows by its name.
func (a *app) fileButton(c *ui.Context, path *string, label string) {
	text := label + "…"
	if *path != "" {
		text = filepath.Base(*path)
	}
	ui.Button(c, text).Tooltip(*path).OnClick(func() { a.chooseFile(func(p string) { *path = p }) })
}
