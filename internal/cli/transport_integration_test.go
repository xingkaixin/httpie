package cli

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTLSCipherSelection(t *testing.T) {
	cli := isolatedCLI(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "selected") }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}}
	server.StartTLS()
	defer server.Close()
	for _, test := range []struct {
		cipher  string
		success bool
	}{
		{"ECDHE+AESGCM:!AES256:@SECLEVEL=2", true},
		{"ECDHE-RSA-AES256-GCM-SHA384", false},
		{"no-such-cipher", false},
	} {
		code, body, diagnostic := cli("--verify=no", "--ssl=tls1.2", "--ciphers="+test.cipher, server.URL)
		if (code == 0) != test.success || (test.success && body != "selected") {
			t.Fatalf("%s: %d %q %s", test.cipher, code, body, diagnostic)
		}
	}
}

func TestHTTPSProxyTunnel(t *testing.T) {
	cli := isolatedCLI(t)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "tunneled") }))
	defer origin.Close()
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprint(secure), func(t *testing.T) {
			received := make(chan string, 1)
			proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" {
					http.Error(w, "CONNECT required", 400)
					return
				}
				upstream, err := net.Dial("tcp", r.Host)
				if err != nil {
					http.Error(w, err.Error(), 502)
					return
				}
				defer upstream.Close()
				received <- r.Header.Get("Proxy-Authorization")
				downstream, buffered, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer downstream.Close()
				fmt.Fprint(downstream, "HTTP/1.1 200 Connection established\r\n\r\n")
				finished := make(chan struct{})
				go func() { io.Copy(upstream, buffered); upstream.Close(); close(finished) }()
				io.Copy(downstream, upstream)
				downstream.Close()
				<-finished
			}))
			if secure {
				proxy.StartTLS()
			} else {
				proxy.Start()
			}
			defer proxy.Close()
			address := strings.Replace(proxy.URL, "://", "://user:pass@", 1)
			code, body, diagnostic := cli("--verify=no", "--proxy=https:"+address, origin.URL)
			if code != 0 || body != "tunneled" {
				t.Fatalf("%d %q %s", code, body, diagnostic)
			}
			if auth := <-received; auth != "Basic dXNlcjpwYXNz" {
				t.Fatalf("proxy authorization: %s", auth)
			}
		})
	}
}

func TestHeadersDoNotWaitForBody(t *testing.T) {
	cli := isolatedCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	start := time.Now()
	code, body, diagnostic := cli("--headers", "--timeout=1", server.URL)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("headers waited for the body")
	}
	if code != 0 || !strings.Contains(body, "200 OK") {
		t.Fatalf("%d %q %s", code, body, diagnostic)
	}
}
