package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/youmark/pkcs8"
	"golang.org/x/net/http/httpproxy"
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

type transport struct {
	options options
	tls     *tls.Config
	conn    net.Conn
	reader  *bufio.Reader
	target  string
}

func (t *transport) Close() {
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
	t.target = ""
}
func newTransport(o options) (*transport, error) {
	config, err := tlsConfig(o)
	if err != nil {
		return nil, err
	}
	return &transport{options: o, tls: config}, nil
}
func (t *transport) proxyFor(u *url.URL) (*url.URL, error) {
	for i := len(t.options.values["proxy"]) - 1; i >= 0; i-- {
		scheme, raw, ok := strings.Cut(t.options.values["proxy"][i], ":")
		if !ok {
			return nil, fmt.Errorf("invalid proxy specification")
		}
		if scheme == u.Scheme || scheme == "all" {
			if raw == "" {
				return nil, nil
			}
			return url.Parse(raw)
		}
	}
	env := httpproxy.FromEnvironment()
	if env.HTTPProxy == "" {
		env.HTTPProxy = os.Getenv("ALL_PROXY")
		if env.HTTPProxy == "" {
			env.HTTPProxy = os.Getenv("all_proxy")
		}
	}
	if env.HTTPSProxy == "" {
		env.HTTPSProxy = os.Getenv("ALL_PROXY")
		if env.HTTPSProxy == "" {
			env.HTTPSProxy = os.Getenv("all_proxy")
		}
	}
	return env.ProxyFunc()(u)
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
	timeout := time.Duration(seconds * float64(time.Second))
	dialer := &net.Dialer{Timeout: timeout}
	destination := address(u)
	if proxyURL != nil {
		destination = address(proxyURL)
	}
	var conn net.Conn
	var err error
	if proxyURL != nil && strings.HasPrefix(proxyURL.Scheme, "socks") {
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
	conn = &idleConn{Conn: conn, timeout: timeout}
	closeOnError := func(err error) error { conn.Close(); return err }
	if proxyURL != nil && proxyURL.Scheme == "https" {
		cfg := t.tls.Clone()
		cfg.ServerName = proxyURL.Hostname()
		secured := tls.Client(conn, cfg)
		if err := secured.HandshakeContext(ctx); err != nil {
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
		cfg := t.tls.Clone()
		cfg.ServerName = u.Hostname()
		secured := tls.Client(conn, cfg)
		if err := secured.HandshakeContext(ctx); err != nil {
			return closeOnError(err)
		}
		conn = secured
	}
	t.conn = conn
	t.reader = bufio.NewReader(conn)
	t.target = target
	return nil
}
func (t *transport) send(ctx context.Context, r *request) (*http.Response, []header, error) {
	p, err := t.proxyFor(r.URL)
	if err != nil {
		return nil, nil, err
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
			raw.WriteString(line)
			if line == "\r\n" || line == "\n" {
				break
			}
			if lineNumber == 0 {
				continue
			}
			name, value, ok := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
			if !ok {
				return nil, nil, fmt.Errorf("malformed response header")
			}
			headers = append(headers, header{name, strings.TrimSpace(value)})
			if max := t.options.integer("max-headers"); max > 0 && len(headers) > max {
				return nil, nil, fmt.Errorf("got more than %d headers", max)
			}
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

func tlsConfig(o options) (*tls.Config, error) {
	c := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	verify := o.value("verify")
	c.InsecureSkipVerify = strings.EqualFold(verify, "no") || strings.EqualFold(verify, "false")
	if verify == "yes" || verify == "true" {
		verify = os.Getenv("REQUESTS_CA_BUNDLE")
		if verify == "" {
			verify = os.Getenv("CURL_CA_BUNDLE")
		}
	}
	if verify != "" && verify != "no" && verify != "false" && verify != "yes" && verify != "true" {
		bundle, err := os.ReadFile(expandPath(verify))
		if err != nil {
			return nil, err
		}
		c.RootCAs = x509.NewCertPool()
		if !c.RootCAs.AppendCertsFromPEM(bundle) {
			return nil, fmt.Errorf("no CA certificates in %s", verify)
		}
	}
	switch o.value("ssl") {
	case "", "ssl2.3":
	case "tls1":
		c.MinVersion = tls.VersionTLS10
		c.MaxVersion = tls.VersionTLS10
	case "tls1.1":
		c.MinVersion = tls.VersionTLS11
		c.MaxVersion = tls.VersionTLS11
	case "tls1.2":
		c.MaxVersion = tls.VersionTLS12
	case "tls1.3":
		c.MinVersion = tls.VersionTLS13
	default:
		return nil, fmt.Errorf("unsupported SSL version: %s", o.value("ssl"))
	}
	if o.has("ciphers") {
		ids, err := cipherSuites(o.value("ciphers"))
		if err != nil {
			return nil, err
		}
		c.CipherSuites = ids
	}
	if o.has("cert") {
		cert, err := os.ReadFile(expandPath(o.value("cert")))
		if err != nil {
			return nil, err
		}
		key := cert
		if o.has("cert-key") {
			key, err = os.ReadFile(expandPath(o.value("cert-key")))
			if err != nil {
				return nil, err
			}
		}
		if block, _ := pem.Decode(key); block != nil && (block.Type == "ENCRYPTED PRIVATE KEY" || x509.IsEncryptedPEMBlock(block)) {
			password := o.value("cert-key-pass")
			if !o.has("cert-key-pass") {
				password, err = readPassword("Password for client private key: ")
				if err != nil {
					return nil, err
				}
			}
			if block.Type == "ENCRYPTED PRIVATE KEY" {
				private, e := pkcs8.ParsePKCS8PrivateKey(block.Bytes, []byte(password))
				if e != nil {
					return nil, e
				}
				der, e := x509.MarshalPKCS8PrivateKey(private)
				if e != nil {
					return nil, e
				}
				key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
			} else {
				der, e := x509.DecryptPEMBlock(block, []byte(password))
				if e != nil {
					return nil, e
				}
				key = pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: der})
			}
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, err
		}
		c.Certificates = []tls.Certificate{pair}
	}
	return c, nil
}

func cipherSuites(expression string) ([]uint16, error) {
	all := append(tls.CipherSuites(), tls.InsecureCipherSuites()...)
	var ids []uint16
	excluded := map[uint16]bool{}
	for _, part := range strings.FieldsFunc(expression, func(r rune) bool { return r == ':' || r == ',' || r == ' ' }) {
		if strings.HasPrefix(part, "@SECLEVEL=") {
			continue
		}
		remove := strings.HasPrefix(part, "!") || strings.HasPrefix(part, "-")
		name := strings.TrimLeft(part, "!-+")
		var matches []uint16
		for _, suite := range all {
			if suite.ID >= 0x1301 && suite.ID <= 0x1303 {
				continue
			}
			openssl := strings.TrimPrefix(suite.Name, "TLS_")
			openssl = strings.ReplaceAll(openssl, "_WITH_", "-")
			openssl = strings.ReplaceAll(openssl, "AES_128", "AES128")
			openssl = strings.ReplaceAll(openssl, "AES_256", "AES256")
			openssl = strings.ReplaceAll(openssl, "_", "-")
			openssl = strings.TrimPrefix(openssl, "RSA-")
			openssl = strings.ReplaceAll(openssl, "-CBC-", "-")
			match := name == suite.Name || name == openssl
			switch name {
			case "DEFAULT", "HIGH", "ALL":
				match = !suite.Insecure
			case "ECDHE":
				match = strings.Contains(suite.Name, "ECDHE")
			case "AESGCM":
				match = strings.Contains(suite.Name, "GCM")
			case "AES128":
				match = strings.Contains(suite.Name, "AES_128")
			case "AES256":
				match = strings.Contains(suite.Name, "AES_256")
			case "CHACHA20":
				match = strings.Contains(suite.Name, "CHACHA20")
			case "RSA":
				match = strings.Contains(suite.Name, "RSA")
			case "ECDSA":
				match = strings.Contains(suite.Name, "ECDSA")
			case "3DES":
				match = strings.Contains(suite.Name, "3DES")
			case "RC4":
				match = strings.Contains(suite.Name, "RC4")
			}
			if match {
				matches = append(matches, suite.ID)
			}
		}
		for _, id := range matches {
			if remove {
				if strings.HasPrefix(part, "!") {
					excluded[id] = true
				}
				for i := 0; i < len(ids); i++ {
					if ids[i] == id {
						ids = append(ids[:i], ids[i+1:]...)
						i--
					}
				}
			} else if !excluded[id] {
				found := false
				for _, existing := range ids {
					if existing == id {
						found = true
					}
				}
				if !found {
					ids = append(ids, id)
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no cipher can be selected from %q", expression)
	}
	return ids, nil
}
