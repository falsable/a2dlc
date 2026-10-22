package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// echoServer answers every connection by echoing its bytes back: a stand-in for the allowed upstream.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln
}

// newTestProxy serves p on a loopback port. dial replaces the upstream dialer and records the addresses it was asked for.
func newTestProxy(t *testing.T, allow map[string]bool, upstream func(addr string) (net.Conn, error)) (addr string, dialed *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	p := &Proxy{allow: allow, dial: func(_ context.Context, a string) (net.Conn, error) {
		mu.Lock()
		calls = append(calls, a)
		mu.Unlock()
		return upstream(a)
	}}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), &calls
}

// connect sends a raw request line to the proxy and returns the status code and the open connection.
func connect(t *testing.T, proxyAddr, method, target string) (int, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: %s\r\n\r\n", method, target, target)
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var code int
	if _, err := fmt.Sscanf(line, "HTTP/1.1 %d", &code); err != nil {
		t.Fatalf("status line %q: %v", line, err)
	}
	return code, c, r
}

func TestAllowedHostTunnelsToPort443(t *testing.T) {
	echo := echoServer(t)
	addr, dialed := newTestProxy(t, map[string]bool{"allowed.test": true}, func(string) (net.Conn, error) {
		return net.Dial("tcp", echo.Addr().String())
	})
	code, c, r := connect(t, addr, http.MethodConnect, "allowed.test:443")
	if code != 200 {
		t.Fatalf("CONNECT allowed.test:443 = %d, wanted 200", code)
	}
	// the blank line that ends the response headers
	if _, err := r.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping" {
		t.Errorf("tunnel returned %q, wanted the bytes written through it", got)
	}
	if len(*dialed) != 1 || (*dialed)[0] != "allowed.test:443" {
		t.Errorf("dialed %v, wanted [allowed.test:443]", *dialed)
	}
}

func TestHostMatchIsCaseInsensitiveAndExact(t *testing.T) {
	echo := echoServer(t)
	allow := map[string]bool{"allowed.test": true}
	addr, _ := newTestProxy(t, allow, func(string) (net.Conn, error) { return net.Dial("tcp", echo.Addr().String()) })
	if code, _, _ := connect(t, addr, http.MethodConnect, "ALLOWED.test:443"); code != 200 {
		t.Errorf("CONNECT ALLOWED.test:443 = %d, wanted 200", code)
	}
	for _, target := range []string{"sub.allowed.test:443", "allowed.test.evil.test:443", "notallowed.test:443"} {
		if code, _, _ := connect(t, addr, http.MethodConnect, target); code != 403 {
			t.Errorf("CONNECT %s = %d, wanted 403 (no suffix matching)", target, code)
		}
	}
}

func TestOtherHostsAreRefusedWithoutDialing(t *testing.T) {
	addr, dialed := newTestProxy(t, map[string]bool{"allowed.test": true}, func(string) (net.Conn, error) {
		t.Errorf("the proxy dialed an address it must refuse")
		return nil, fmt.Errorf("unused")
	})
	for _, target := range []string{
		"other.test:443",    // not on the list
		"allowed.test:80",   // allowed host, other port
		"allowed.test:8443", // allowed host, other port
		"[::1]:443",         // IP literal
		"127.0.0.1:443",     // IP literal, loopback
		"allowed.test",      // no port
	} {
		if code, _, _ := connect(t, addr, http.MethodConnect, target); code != 403 {
			t.Errorf("CONNECT %s = %d, wanted 403", target, code)
		}
	}
	if len(*dialed) != 0 {
		t.Errorf("dialed %v for refused targets", *dialed)
	}
}

func TestPlainHTTPIsNotProxied(t *testing.T) {
	addr, dialed := newTestProxy(t, map[string]bool{"allowed.test": true}, func(string) (net.Conn, error) {
		t.Errorf("a plain HTTP request was dialed")
		return nil, fmt.Errorf("unused")
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET http://allowed.test/ HTTP/1.1\r\nHost: allowed.test\r\n\r\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "HTTP/1.1 405") {
		t.Errorf("GET = %q, wanted 405", line)
	}
	if len(*dialed) != 0 {
		t.Errorf("dialed %v for a plain HTTP request", *dialed)
	}
}

func TestParseAllow(t *testing.T) {
	allow, err := ParseAllow(" OpenRouter.ai, api.anthropic.com,, ")
	if err != nil {
		t.Fatal(err)
	}
	if len(allow) != 2 || !allow["openrouter.ai"] || !allow["api.anthropic.com"] {
		t.Errorf("ParseAllow = %v", allow)
	}
	if empty, err := ParseAllow(""); err != nil || len(empty) != 0 {
		t.Errorf("ParseAllow(\"\") = %v, %v; wanted an empty list", empty, err)
	}
	for _, bad := range []string{"*.test", "a b.test", "host:443", "http://x.test"} {
		if _, err := ParseAllow(bad); err == nil {
			t.Errorf("ParseAllow(%q) accepted a non-hostname", bad)
		}
	}
}

// An upstream that cannot be dialed is a 502 and the tunnel is not opened.
func TestUnreachableUpstreamIsBadGateway(t *testing.T) {
	addr, dialed := newTestProxy(t, map[string]bool{"allowed.test": true}, func(string) (net.Conn, error) {
		return nil, fmt.Errorf("connection refused")
	})
	if code, _, _ := connect(t, addr, http.MethodConnect, "allowed.test:443"); code != 502 {
		t.Errorf("CONNECT with an unreachable upstream = %d, wanted 502", code)
	}
	if len(*dialed) != 1 {
		t.Errorf("dialed %v, wanted one attempt", *dialed)
	}
}

// A writer that cannot be hijacked (no connection to take over) is a 500, and the upstream it dialed is closed.
func TestNonHijackableWriterIsInternalError(t *testing.T) {
	closed := make(chan struct{})
	p := &Proxy{allow: map[string]bool{"allowed.test": true}, dial: func(context.Context, string) (net.Conn, error) {
		c1, _ := net.Pipe()
		return &closeNotifier{Conn: c1, done: closed}, nil
	}}
	req := httptest.NewRequest(http.MethodConnect, "allowed.test:443", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("CONNECT through a non-hijackable writer = %d, wanted 500", rec.Code)
	}
	select {
	case <-closed:
	default:
		t.Error("the dialed upstream was not closed")
	}
}

// closeNotifier closes done once, the first time the connection is closed.
type closeNotifier struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *closeNotifier) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
