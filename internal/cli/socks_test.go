package cli

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSOCKSProxies(t *testing.T) {
	cli := isolatedCLI(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "proxied", r.Header.Get("Proxy-Authorization"))
	}))
	defer origin.Close()
	for _, protocol := range []string{"socks4", "socks4a", "socks5", "socks5h"} {
		t.Run(protocol, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			result := make(chan error, 1)
			go func() { result <- serveSOCKS(listener, strings.TrimPrefix(origin.URL, "http://"), protocol) }()
			target := origin.URL
			if protocol == "socks4a" || protocol == "socks5h" {
				target = strings.Replace(target, "127.0.0.1", "origin.invalid", 1)
			}
			code, body, diagnostic := cli("--timeout=1", "--proxy=http:"+protocol+"://user:pass@"+listener.Addr().String(), target)
			if code != 0 || body != "proxied" {
				t.Fatalf("%d %q %s", code, body, diagnostic)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveSOCKS(listener net.Listener, origin, protocol string) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)
	if strings.HasPrefix(protocol, "socks4") {
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader, header); err != nil {
			return err
		}
		if header[0] != 4 || header[1] != 1 {
			return fmt.Errorf("invalid SOCKS4 request: %v", header)
		}
		user, err := reader.ReadString(0)
		if err != nil || user != "user\x00" {
			return fmt.Errorf("SOCKS4 user: %q %v", user, err)
		}
		if protocol == "socks4a" {
			host, err := reader.ReadString(0)
			if err != nil || host != "origin.invalid\x00" {
				return fmt.Errorf("SOCKS4a host: %q %v", host, err)
			}
		}
		conn.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0})
	} else {
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(reader, greeting); err != nil {
			return err
		}
		if greeting[0] != 5 {
			return fmt.Errorf("invalid SOCKS5 greeting")
		}
		if _, err := io.CopyN(io.Discard, reader, int64(greeting[1])); err != nil {
			return err
		}
		conn.Write([]byte{5, 2})
		if _, err := io.ReadFull(reader, greeting); err != nil {
			return err
		}
		user := make([]byte, greeting[1])
		if _, err := io.ReadFull(reader, user); err != nil {
			return err
		}
		count, err := reader.ReadByte()
		if err != nil {
			return err
		}
		password := make([]byte, count)
		if _, err := io.ReadFull(reader, password); err != nil {
			return err
		}
		if string(user) != "user" || string(password) != "pass" {
			return fmt.Errorf("invalid SOCKS5 credentials")
		}
		conn.Write([]byte{1, 0})
		request := make([]byte, 4)
		if _, err := io.ReadFull(reader, request); err != nil {
			return err
		}
		length := 4
		if request[3] == 3 {
			n, err := reader.ReadByte()
			if err != nil {
				return err
			}
			length = int(n)
		}
		host := make([]byte, length)
		if _, err := io.ReadFull(reader, host); err != nil {
			return err
		}
		if protocol == "socks5h" && string(host) != "origin.invalid" {
			return fmt.Errorf("SOCKS5h host: %q", host)
		}
		if _, err := io.CopyN(io.Discard, reader, 2); err != nil {
			return err
		}
		conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
	}
	upstream, err := net.Dial("tcp", origin)
	if err != nil {
		return err
	}
	defer upstream.Close()
	done := make(chan struct{})
	go func() { io.Copy(upstream, reader); upstream.Close(); close(done) }()
	io.Copy(conn, upstream)
	conn.Close()
	<-done
	return nil
}

func TestSOCKSHandshakeTimeout(t *testing.T) {
	cli := isolatedCLI(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(150 * time.Millisecond)
	}()
	code, _, diagnostic := cli("--timeout=.025", "--proxy=http:socks5h://"+listener.Addr().String(), "http://origin.invalid")
	if code != 2 {
		t.Fatalf("timeout exit=%d: %s", code, diagnostic)
	}
}
