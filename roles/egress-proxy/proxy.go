package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// hostName is one allowlist entry: the characters the roles.json egress_allow pattern accepts.
var hostName = regexp.MustCompile(`^[a-z0-9.-]+$`)

// Proxy tunnels HTTP CONNECT requests to port 443 of allowlisted hosts and answers 403 to everything else. Plain
// HTTP requests are refused (405): the adapter's providers are reached over HTTPS only.
type Proxy struct {
	allow map[string]bool
	dial  func(ctx context.Context, addr string) (net.Conn, error)
}

// ParseAllow reads the comma-separated allowlist from A2DLC_EGRESS_ALLOW. Names are lowercased; an empty list allows
// nothing, and a malformed name is an error so the proxy never starts with a wider list than the launcher sent.
func ParseAllow(list string) (map[string]bool, error) {
	allow := map[string]bool{}
	for _, h := range strings.Split(list, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !hostName.MatchString(h) {
			return nil, fmt.Errorf("allowlist entry %q is not a hostname", h)
		}
		allow[h] = true
	}
	return allow, nil
}

// NewProxy returns a proxy that dials with the system resolver and a 10 s timeout.
func NewProxy(allow map[string]bool) *Proxy {
	var d net.Dialer
	return &Proxy{
		allow: allow,
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return d.DialContext(ctx, "tcp", addr)
		},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is proxied", http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	host = strings.ToLower(host)
	if err != nil || port != "443" || !p.allow[host] {
		log.Printf("deny CONNECT %s", r.Host)
		http.Error(w, "host not on the egress allowlist", http.StatusForbidden)
		return
	}
	upstream, err := p.dial(r.Context(), net.JoinHostPort(host, port))
	if err != nil {
		log.Printf("allow CONNECT %s: upstream unreachable", r.Host)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	client, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		upstream.Close()
		http.Error(w, "CONNECT needs a hijackable connection", http.StatusInternalServerError)
		return
	}
	log.Printf("allow CONNECT %s", r.Host)
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		upstream.Close()
		client.Close()
		return
	}
	tunnel(client, upstream, buf)
}

// tunnel copies both ways until either side ends, then closes both. buf holds bytes the client sent before the
// hijack; they go upstream first.
func tunnel(client, upstream net.Conn, buf io.Reader) {
	var once sync.Once
	closeBoth := func() {
		client.Close()
		upstream.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, buf)
		once.Do(closeBoth)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		once.Do(closeBoth)
	}()
	wg.Wait()
}
