package cli

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youmark/pkcs8"
)

func isolatedCLI(t *testing.T) func(...string) (int, string, string) {
	t.Helper()
	t.Setenv("HTTPIE_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(configDir(), "config.json"), []byte(`{"disable_update_warnings":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, "")
	}
	return func(args ...string) (int, string, string) {
		var out, stderr bytes.Buffer
		code, err := run("http", append([]string{"-I"}, args...), strings.NewReader(""), &out, &stderr, true, false, false)
		if err != nil {
			fmt.Fprint(&stderr, err)
		}
		return code, out.String(), stderr.String()
	}
}
func TestHTTPBehavior(t *testing.T) {
	cli := isolatedCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/echo", 307)
		case "/echo":
			body, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "%s %s %s", r.Method, r.Header.Get("X-Test"), body)
		case "/gzip":
			w.Header().Set("Content-Encoding", "gzip")
			z := gzip.NewWriter(w)
			fmt.Fprint(z, "decoded")
			z.Close()
		case "/slow":
			time.Sleep(60 * time.Millisecond)
			fmt.Fprint(w, "late")
		case "/stream":
			for range 4 {
				fmt.Fprint(w, "line\n")
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cases := []struct {
		args []string
		code int
		body string
	}{
		{[]string{"-F", server.URL + "/redirect", "X-Test:value", "a=1"}, 0, `POST value {"a": "1"}`},
		{[]string{"--chunked", server.URL + "/echo", "--raw=chunk"}, 0, "POST  chunk"},
		{[]string{server.URL + "/gzip"}, 0, "decoded"},
		{[]string{"--check-status", server.URL + "/missing"}, 4, ""},
		{[]string{"--timeout=.025", server.URL + "/slow"}, 2, ""},
		{[]string{"--timeout=.04", "--stream", server.URL + "/stream"}, 0, "line\nline\nline\nline\n"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			code, body, err := cli(c.args...)
			if code != c.code || body != c.body {
				t.Fatalf("got %d %q (%s), want %d %q", code, body, err, c.code, c.body)
			}
		})
	}
}
func TestTLSVerificationAndClientCertificate(t *testing.T) {
	cli := isolatedCLI(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secure") }))
	defer server.Close()
	cert := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, verify := range []string{"yes", "no", cert} {
		code, body, err := cli("--verify="+verify, server.URL)
		if verify == "yes" {
			if code == 0 {
				t.Fatal("untrusted certificate accepted")
			}
		} else if code != 0 || body != "secure" {
			t.Fatalf("%s: %d %q %s", verify, code, body, err)
		}
	}
	pair := server.TLS.Certificates[0]
	mtls := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "mutual") }))
	mtls.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAnyClientCert}
	mtls.StartTLS()
	defer mtls.Close()
	code, _, _ := cli("--verify=no", mtls.URL)
	if code == 0 {
		t.Fatal("mTLS accepted missing client certificate")
	}
	key, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	combined := filepath.Join(t.TempDir(), "client.pem")
	certificate, _ := os.ReadFile(cert)
	if err = os.WriteFile(combined, append(certificate, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...), 0600); err != nil {
		t.Fatal(err)
	}
	if code, body, diagnostic := cli("--verify=no", "--cert="+combined, mtls.URL); code != 0 || body != "mutual" {
		t.Fatalf("client certificate: %d %q %s", code, body, diagnostic)
	}
	encrypted, err := pkcs8.MarshalPrivateKey(pair.PrivateKey, []byte("secret"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(combined, append(certificate, pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: encrypted})...), 0600); err != nil {
		t.Fatal(err)
	}
	if code, body, diagnostic := cli("--verify=no", "--cert="+combined, "--cert-key-pass=secret", mtls.URL); code != 0 || body != "mutual" {
		t.Fatalf("encrypted client key: %d %q %s", code, body, diagnostic)
	}
}
func TestSessionPersistenceAndReadOnly(t *testing.T) {
	cli := isolatedCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "token", Value: "secret", Path: "/"})
		}
		fmt.Fprintf(w, "%s|%s|%s", r.Header.Get("X-Session"), r.Header.Get("Cookie"), r.Header.Get("Authorization"))
	}))
	defer server.Close()
	filename := filepath.Join(t.TempDir(), "session.json")
	if code, _, err := cli("--session="+filename, "-auser:pass", server.URL+"/set", "X-Session:persist"); code != 0 {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	code, body, diagnostic := cli("--session-read-only="+filename, server.URL, "X-Session:override")
	if code != 0 || body != "override|token=secret|Basic dXNlcjpwYXNz" {
		t.Fatalf("%d %q %s", code, body, diagnostic)
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only session was modified")
	}
}
func TestDownloadResumeAndRangeRejection(t *testing.T) {
	cli := isolatedCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=5-" {
			if r.URL.Path == "/bad" {
				w.Header().Set("Content-Range", "bytes 4-9/10")
			} else {
				w.Header().Set("Content-Range", "bytes 5-9/10")
			}
			w.WriteHeader(206)
			fmt.Fprint(w, "56789")
			return
		}
		fmt.Fprint(w, "0123456789")
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "download")
	for _, path := range []string{"/bad", "/good"} {
		if err := os.WriteFile(file, []byte("01234"), 0600); err != nil {
			t.Fatal(err)
		}
		code, _, err := cli("-dco", file, server.URL+path)
		data, _ := os.ReadFile(file)
		if path == "/bad" {
			if code == 0 || string(data) != "01234" {
				t.Fatalf("invalid range modified download: %d %s %s", code, data, err)
			}
		} else if code != 0 || string(data) != "0123456789" {
			t.Fatalf("resume: %d %s %s", code, data, err)
		}
	}
}
func TestDigestAuthentication(t *testing.T) {
	cli := isolatedCLI(t)
	hash := func(s string) string { v := md5.Sum([]byte(s)); return hex.EncodeToString(v[:]) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="basic", Digest realm="test", nonce="nonce", qop="auth", algorithm=MD5`)
			w.Header().Set("Set-Cookie", "challenge=seen; Path=/")
			w.WriteHeader(401)
			return
		}
		fields := map[string]string{}
		for _, m := range digestField.FindAllStringSubmatch(auth, -1) {
			v := m[2]
			if v == "" {
				v = m[3]
			}
			fields[m[1]] = v
		}
		expected := hash(hash("user:test:password") + ":nonce:" + fields["nc"] + ":" + fields["cnonce"] + ":auth:" + hash(r.Method+":"+r.URL.RequestURI()))
		if fields["response"] != expected || r.Header.Get("Cookie") != "token=secret" {
			w.WriteHeader(403)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	}))
	defer server.Close()
	code, body, err := cli("--check-status", "-A", "digest", "-auser:password", server.URL, "Cookie:token=secret", "--raw=repeatable body")
	if code != 0 || body != "repeatable body" {
		t.Fatalf("digest: %d %q %s", code, body, err)
	}
}
func TestCrossHostRedirectStripsCredentials(t *testing.T) {
	cli := isolatedCLI(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.Header.Get("Authorization")) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	code, body, err := cli("-F", "-auser:pass", source.URL)
	if code != 0 || body != "" {
		t.Fatalf("credentials leaked: %d %q %s", code, body, err)
	}
}
func TestProxyAndHeaderLimit(t *testing.T) {
	cli := isolatedCLI(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s", r.URL.String(), r.Header.Get("Proxy-Authorization"))
	}))
	defer proxy.Close()
	code, body, err := cli("--proxy=http:"+strings.Replace(proxy.URL, "://", "://user:pass@", 1), "http://example.invalid/path")
	if code != 0 || body != "http://example.invalid/path|Basic dXNlcjpwYXNz" {
		t.Fatalf("proxy: %d %q %s", code, body, err)
	}
	listener, err2 := net.Listen("tcp", "127.0.0.1:0")
	if err2 != nil {
		t.Fatal(err2)
	}
	defer listener.Close()
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		request := make([]byte, 4096)
		conn.Read(request)
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nX-One: a\r\nX-Two: b\r\nContent-Length: 0\r\n\r\n")
	}()
	code, _, _ = cli("--max-headers=1", "http://"+listener.Addr().String())
	if code != 1 {
		t.Fatalf("header limit exit=%d", code)
	}
}

func TestStdinStartsBeforeEOF(t *testing.T) {
	for _, flags := range [][]string{{"--chunked"}, {"Content-Length:11"}} {
		t.Run(flags[0], func(t *testing.T) {
			isolatedCLI(t)
			received := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				first := make([]byte, len("first\n"))
				_, err := io.ReadFull(r.Body, first)
				if err == nil {
					received <- string(first)
				}
				rest, _ := io.ReadAll(r.Body)
				_, _ = w.Write(append(first, rest...))
			}))
			defer server.Close()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			done := make(chan error, 1)
			go func() {
				var output bytes.Buffer
				code, err := run("http", append([]string{"--timeout=2", "-pb", server.URL}, flags...), reader, &output, io.Discard, false, false, false)
				if err == nil && (code != 0 || output.String() != "first\nlast\n") {
					err = fmt.Errorf("code=%d output=%q", code, output.String())
				}
				done <- err
			}()
			_, _ = io.WriteString(writer, "first\n")
			select {
			case data := <-received:
				if data != "first\n" {
					t.Errorf("first chunk = %q", data)
				}
			case <-time.After(time.Second):
				t.Error("the server received no data before stdin EOF")
			}
			_, _ = io.WriteString(writer, "last\n")
			writer.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
