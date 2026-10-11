package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(p)
}
func (c *idleConn) Write(p []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.SetWriteDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Write(p)
}

type idleDialer struct{ net.Dialer }

func (d *idleDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}
func (d *idleDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &idleConn{Conn: conn, timeout: d.Timeout}, nil
}

type transport struct {
	options options
	tls     *tlsSettings
	conn    net.Conn
	reader  *bufio.Reader
	target  string
	cancel  func() bool
}

func (t *transport) Close() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
	t.target = ""
}
func newTransport(o options) (*transport, error) {
	config, err := newTLSSettings(o)
	if err != nil {
		return nil, err
	}
	return &transport{options: o, tls: config}, nil
}
func (t *transport) proxyFor(u *url.URL) (*url.URL, error) {
	proxies := map[string]string{}
	for _, value := range t.options.values["proxy"] {
		key, value, ok := splitCredentials(value)
		if !ok {
			return nil, fmt.Errorf("invalid proxy specification")
		}
		proxies[key] = value
	}
	noProxy := os.Getenv("no_proxy")
	if noProxy == "" {
		noProxy = os.Getenv("NO_PROXY")
	}
	if value, ok := proxies["no_proxy"]; ok {
		noProxy = value
	}
	if !bypassProxy(u, noProxy) {
		for _, key := range []string{"http", "https", "all"} {
			if _, exists := proxies[key]; exists {
				continue
			}
			value, lower := os.LookupEnv(key + "_proxy")
			if !lower && (key != "http" || os.Getenv("REQUEST_METHOD") == "") {
				value = os.Getenv(strings.ToUpper(key) + "_PROXY")
			}
			if value != "" {
				proxies[key] = value
			}
		}
	}
	for _, key := range []string{u.Scheme + "://" + u.Hostname(), u.Scheme, "all://" + u.Hostname(), "all"} {
		if raw, ok := proxies[key]; ok {
			if raw == "" {
				return nil, nil
			}
			if !strings.Contains(raw, "://") {
				raw = "http://" + raw
			}
			return url.Parse(raw)
		}
	}
	return nil, nil
}

func bypassProxy(u *url.URL, rules string) bool {
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	hostWithPort := host
	if u.Port() != "" {
		hostWithPort += ":" + u.Port()
	}
	for _, rule := range strings.Split(strings.ReplaceAll(rules, " ", ""), ",") {
		rule = strings.ToLower(rule)
		if rule == "*" {
			return true
		}
		if rule == "" {
			continue
		}
		if ip.To4() != nil {
			if _, network, err := net.ParseCIDR(rule); err == nil && network.IP.To4() != nil && network.Contains(ip) || host == rule {
				return true
			}
			continue
		}
		rule = strings.TrimLeft(rule, ".")
		if host == rule || hostWithPort == rule || strings.HasSuffix(host, "."+rule) || strings.HasSuffix(hostWithPort, "."+rule) {
			return true
		}
	}
	return false
}
func address(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}
func (t *transport) connect(ctx context.Context, u, proxyURL *url.URL) error {
	proxyKey := ""
	if proxyURL != nil {
		proxyKey = proxyURL.String()
	}
	target := u.Scheme + "://" + u.Host + "|" + proxyKey
	if t.conn != nil && t.target == target {
		return nil
	}
	t.Close()
	seconds, _ := strconv.ParseFloat(t.options.value("timeout"), 64)
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return fmt.Errorf("invalid timeout: %s", t.options.value("timeout"))
	}
	timeout := time.Duration(seconds * float64(time.Second))
	dialer := &idleDialer{Dialer: net.Dialer{Timeout: timeout}}
	destination := address(u)
	if proxyURL != nil {
		destination = address(proxyURL)
	}
	var conn net.Conn
	var err error
	if proxyURL != nil && (proxyURL.Scheme == "socks4" || proxyURL.Scheme == "socks4a") {
		conn, err = dialer.DialContext(ctx, "tcp", destination)
		if err == nil {
			connection := conn
			stop := context.AfterFunc(ctx, func() { connection.Close() })
			err = socks4Connect(ctx, conn, u, proxyURL)
			stop()
			if err != nil {
				conn.Close()
			}
		}
	} else if proxyURL != nil && strings.HasPrefix(proxyURL.Scheme, "socks") {
		var auth *proxy.Auth
		if proxyURL.User != nil {
			pass, _ := proxyURL.User.Password()
			auth = &proxy.Auth{User: proxyURL.User.Username(), Password: pass}
		}
		d, e := proxy.SOCKS5("tcp", destination, auth, dialer)
		if e != nil {
			return e
		}
		destination = address(u)
		if proxyURL.Scheme == "socks5" {
			addresses, e := net.DefaultResolver.LookupHost(ctx, u.Hostname())
			if e != nil {
				return e
			}
			_, port, _ := net.SplitHostPort(destination)
			destination = net.JoinHostPort(addresses[0], port)
		}
		conn, err = d.(proxy.ContextDialer).DialContext(ctx, "tcp", destination)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", destination)
	}
	if err != nil {
		return err
	}
	closeOnError := func(err error) error { conn.Close(); return err }
	if proxyURL != nil && proxyURL.Scheme == "https" {
		secured, err := t.tls.connect(ctx, conn, proxyURL.Hostname())
		if err != nil {
			return closeOnError(err)
		}
		conn = secured
	}
	if proxyURL != nil && !strings.HasPrefix(proxyURL.Scheme, "socks") && u.Scheme == "https" {
		connect := &http.Request{Method: "CONNECT", URL: &url.URL{Opaque: address(u)}, Host: address(u), Header: make(http.Header)}
		if proxyURL.User != nil {
			pass, _ := proxyURL.User.Password()
			connect.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username()+":"+pass)))
		}
		if err := connect.Write(conn); err != nil {
			return closeOnError(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), connect)
		if err != nil {
			return closeOnError(err)
		}
		if response.StatusCode != 200 {
			return closeOnError(fmt.Errorf("proxy CONNECT: %s", response.Status))
		}
	}
	if u.Scheme == "https" {
		secured, err := t.tls.connect(ctx, conn, u.Hostname())
		if err != nil {
			return closeOnError(err)
		}
		conn = secured
	}
	t.conn = conn
	t.cancel = context.AfterFunc(ctx, func() { _ = conn.Close() })
	t.reader = bufio.NewReader(conn)
	t.target = target
	return nil
}
func (t *transport) send(ctx context.Context, r *request) (*http.Response, []header, error) {
	p, err := t.proxyFor(r.URL)
	if err != nil {
		return nil, nil, err
	}
	if p != nil {
		switch p.Scheme {
		case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
		default:
			return nil, nil, fmt.Errorf("unsupported proxy scheme: %s", p.Scheme)
		}
	}
	if err = t.connect(ctx, r.URL, p); err != nil {
		return nil, nil, err
	}
	uri := r.URL.RequestURI()
	if p != nil && r.URL.Scheme == "http" && !strings.HasPrefix(p.Scheme, "socks") {
		u := *r.URL
		u.User = nil
		u.Fragment = ""
		uri = u.String()
	}
	w := bufio.NewWriter(t.conn)
	if _, err = fmt.Fprintf(w, "%s %s HTTP/1.1\r\n", r.Method, uri); err != nil {
		return nil, nil, err
	}
	for _, h := range r.headers {
		if _, err = fmt.Fprintf(w, "%s: %s\r\n", h.Name, h.Value); err != nil {
			return nil, nil, err
		}
	}
	if p != nil && p.User != nil && r.URL.Scheme == "http" {
		pass, _ := p.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(p.User.Username() + ":" + pass))
		fmt.Fprintf(w, "Proxy-Authorization: Basic %s\r\n", token)
	}
	if _, err = io.WriteString(w, "\r\n"); err != nil {
		return nil, nil, err
	}
	if r.liveInput {
		if err = w.Flush(); err != nil {
			return nil, nil, err
		}
	}
	if r.Body != nil {
		if len(r.TransferEncoding) > 0 && r.TransferEncoding[0] == "chunked" {
			buf := make([]byte, 32*1024)
			for {
				n, e := r.Body.Read(buf)
				if n > 0 {
					if _, err = fmt.Fprintf(w, "%x\r\n", n); err == nil {
						_, err = w.Write(buf[:n])
					}
					if err == nil {
						_, err = io.WriteString(w, "\r\n")
					}
					if err == nil && r.liveInput {
						err = w.Flush()
					}
					if err != nil {
						break
					}
				}
				if e == io.EOF {
					break
				}
				if e != nil {
					err = e
					break
				}
			}
			if err == nil {
				_, err = io.WriteString(w, "0\r\n\r\n")
			}
		} else {
			_, err = io.Copy(w, r.Body)
		}
		r.Body.Close()
		if err != nil {
			return nil, nil, err
		}
	} else if len(r.TransferEncoding) > 0 {
		_, err = io.WriteString(w, "0\r\n\r\n")
		if err != nil {
			return nil, nil, err
		}
	}
	if err = w.Flush(); err != nil {
		return nil, nil, err
	}
	for {
		var raw bytes.Buffer
		var headers []header
		for lineNumber := 0; ; lineNumber++ {
			line, e := t.reader.ReadString('\n')
			if e != nil {
				return nil, nil, e
			}
			if len(line) > 65536 {
				return nil, nil, fmt.Errorf("response header line exceeds 65536 bytes")
			}
			if max := t.options.integer("max-headers"); max != 0 && lineNumber > max {
				return nil, nil, fmt.Errorf("got more than %d headers", max)
			}
			raw.WriteString(line)
			if line == "\r\n" || line == "\n" {
				break
			}
			if lineNumber == 0 {
				continue
			}
			if (line[0] == ' ' || line[0] == '\t') && len(headers) > 0 {
				headers[len(headers)-1].Value += "\r\n" + strings.TrimRight(line, "\r\n")
				continue
			}
			name, value, ok := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
			if !ok {
				return nil, nil, fmt.Errorf("malformed response header")
			}
			headers = append(headers, header{name, strings.TrimSpace(value)})
		}
		reader := bufio.NewReader(io.MultiReader(bytes.NewReader(raw.Bytes()), t.reader))
		response, e := http.ReadResponse(reader, r.Request)
		if e != nil {
			return nil, nil, e
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 && response.StatusCode != 101 {
			continue
		}
		return response, headers, nil
	}
}

func socks4Connect(ctx context.Context, conn net.Conn, target, server *url.URL) error {
	host := target.Hostname()
	ip := net.ParseIP(host).To4()
	remoteDNS := ip == nil && server.Scheme == "socks4a"
	if remoteDNS {
		ip = net.IPv4(0, 0, 0, 1).To4()
	}
	if ip == nil {
		addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil {
			return err
		}
		if len(addresses) == 0 {
			return fmt.Errorf("SOCKS4 requires an IPv4 destination")
		}
		ip = addresses[0].To4()
	}
	_, portText, _ := net.SplitHostPort(address(target))
	port, _ := strconv.Atoi(portText)
	packet := []byte{4, 1, byte(port >> 8), byte(port)}
	packet = append(packet, ip...)
	if server.User != nil {
		packet = append(packet, server.User.Username()...)
	}
	packet = append(packet, 0)
	if remoteDNS {
		packet = append(append(packet, host...), 0)
	}
	if _, err := conn.Write(packet); err != nil {
		return err
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[1] != 90 {
		return fmt.Errorf("SOCKS4 connection rejected: %d", reply[1])
	}
	return nil
}
