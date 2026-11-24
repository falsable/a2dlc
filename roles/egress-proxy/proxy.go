package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// hostName is one allowlist entry: the characters the roles.json egress_allow pattern accepts.
var hostName = regexp.MustCompile(`^[a-z0-9.-]+$`)
var routeNameRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// Route configures plain-HTTP reverse/forward proxying and credential injection for one allowlisted upstream.
type Route struct {
	Target    string            `json:"target"`
	Headers   map[string]string `json:"headers,omitempty"`
	AllowHTTP bool              `json:"allow_http,omitempty"`

	targetURL *url.URL
	hostPort  string
}

// Proxy tunnels HTTP CONNECT requests to port 443 of allowlisted hosts (plus configured AllowHTTP ports) and,
// when routes are configured, forwards HTTP requests to those routes while re-originating TLS and injecting headers.
type Proxy struct {
	allow     map[string]bool
	routes    map[string]Route
	dial      func(ctx context.Context, addr string) (net.Conn, error)
	transport http.RoundTripper
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

// ParseRoutes parses A2DLC_EGRESS_ROUTES JSON and validates every route's target host is already in allow.
func ParseRoutes(raw string, allow map[string]bool) (map[string]Route, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var parsed map[string]Route
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("A2DLC_EGRESS_ROUTES: %w", err)
	}
	out := make(map[string]Route, len(parsed))
	for name, rt := range parsed {
		if !routeNameRe.MatchString(name) {
			return nil, fmt.Errorf("route name %q must match %s", name, routeNameRe)
		}
		u, err := url.Parse(rt.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, fmt.Errorf("route %q target %q must be http(s)://host[:port]", name, rt.Target)
		}
		host := strings.ToLower(u.Hostname())
		if !allow[host] {
			return nil, fmt.Errorf("route %q target host %q is not in allowlist", name, host)
		}
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		rt.targetURL = u
		rt.hostPort = net.JoinHostPort(host, port)
		out[name] = rt
	}
	return out, nil
}

// NewProxy returns a proxy that dials with the system resolver and a 10 s timeout.
func NewProxy(allow map[string]bool, routes ...map[string]Route) *Proxy {
	var d net.Dialer
	var rtMap map[string]Route
	if len(routes) > 0 {
		rtMap = routes[0]
	}
	return &Proxy{
		allow:  allow,
		routes: rtMap,
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return d.DialContext(ctx, "tcp", addr)
		},
		transport: http.DefaultTransport,
	}
}

func (p *Proxy) allowsHTTPConnect(host, port string) bool {
	hp := net.JoinHostPort(host, port)
	for _, rt := range p.routes {
		if rt.AllowHTTP && rt.hostPort == hp {
			return true
		}
	}
	return false
}

func (p *Proxy) matchHTTPRequest(r *http.Request) (Route, string, bool) {
	if r.URL.IsAbs() {
		reqHost := strings.ToLower(r.URL.Hostname())
		reqPort := r.URL.Port()
		if reqPort == "" {
			if r.URL.Scheme == "https" {
				reqPort = "443"
			} else {
				reqPort = "80"
			}
		}
		hp := net.JoinHostPort(reqHost, reqPort)
		for _, rt := range p.routes {
			if rt.AllowHTTP && rt.hostPort == hp {
				path := r.URL.Path
				if path == "" {
					path = "/"
				}
				return rt, path, true
			}
		}
	}
	if strings.HasPrefix(r.URL.Path, "/r/") {
		rest := strings.TrimPrefix(r.URL.Path, "/r/")
		name, subPath, hasSlash := strings.Cut(rest, "/")
		if rt, ok := p.routes[name]; ok {
			if !hasSlash || subPath == "" {
				return rt, "/", true
			}
			return rt, "/" + subPath, true
		}
		return Route{}, "", false
	}
	if r.URL.IsAbs() {
		return Route{}, "", false
	}
	if len(p.routes) == 1 {
		for _, rt := range p.routes {
			path := r.URL.Path
			if path == "" {
				path = "/"
			}
			return rt, path, true
		}
	}
	return Route{}, "", false
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		if len(p.routes) == 0 {
			http.Error(w, "only CONNECT is proxied", http.StatusMethodNotAllowed)
			return
		}
		rt, upPath, ok := p.matchHTTPRequest(r)
		if !ok {
			log.Printf("deny HTTP %s %s", r.Method, r.URL.String())
			http.Error(w, "route not on the egress allowlist", http.StatusForbidden)
			return
		}
		p.forwardHTTP(w, r, rt, upPath)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	host = strings.ToLower(host)
	if err != nil || !p.allow[host] || (port != "443" && !p.allowsHTTPConnect(host, port)) {
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

var hopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func (p *Proxy) forwardHTTP(w http.ResponseWriter, r *http.Request, rt Route, upPath string) {
	base := rt.targetURL
	if base == nil {
		u, err := url.Parse(rt.Target)
		if err != nil {
			http.Error(w, "invalid route target", http.StatusInternalServerError)
			return
		}
		base = u
	}
	target := *base
	target.Path = strings.TrimRight(base.Path, "/") + upPath
	target.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "invalid upstream request", http.StatusBadRequest)
		return
	}
	for k, vs := range r.Header {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Host = target.Host
	for k, v := range rt.Headers {
		req.Header.Set(k, v)
	}
	tr := p.transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		log.Printf("allow HTTP %s -> %s: upstream unreachable: %v", r.URL.Path, target.Host, err)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	log.Printf("allow HTTP %s -> %s (%d)", r.URL.Path, target.Host, resp.StatusCode)
	if fl, ok := w.(http.Flusher); ok {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				fl.Flush()
			}
			if rerr != nil {
				return
			}
		}
	}
	_, _ = io.Copy(w, resp.Body)
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
