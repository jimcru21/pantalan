// Package proxy is the built-in reverse proxy that routes each app's domain to
// its live container.
//
// TODO: automatic HTTPS (golang.org/x/crypto/acme/autocert or certmagic).
package proxy

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

type Proxy struct {
	log    *slog.Logger
	mu     sync.RWMutex
	routes map[string]route // by lowercased host
}

type route struct {
	upstream string
	proxy    *httputil.ReverseProxy
}

func New(log *slog.Logger) *Proxy {
	return &Proxy{log: log, routes: map[string]route{}}
}

// SetRoute sends requests for host to upstream (host:port). Replacing a route
// is atomic, so in-flight requests finish on the old upstream and new ones go
// to the new one.
func (p *Proxy) SetRoute(host, upstream string) {
	target := &url.URL{Scheme: "http", Host: upstream}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded()
			r.Out.Host = r.In.Host // apps see their public domain, not 127.0.0.1
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log.Warn("upstream error", "host", r.Host, "upstream", upstream, "err", err)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[strings.ToLower(host)] = route{upstream: upstream, proxy: rp}
}

func (p *Proxy) RemoveRoute(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.routes, strings.ToLower(host))
}

// RemoveRoutesTo removes every route to upstream, whatever its host.
func (p *Proxy) RemoveRoutesTo(upstream string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for host, r := range p.routes {
		if r.upstream == upstream {
			delete(p.routes, host)
		}
	}
}

// MoveRoute serves from's upstream under to instead, replacing any route
// to had, in one step. It does nothing when from has no route.
func (p *Proxy) MoveRoute(from, to string) {
	from, to = strings.ToLower(from), strings.ToLower(to)
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.routes[from]
	if !ok || from == to {
		return
	}
	delete(p.routes, from)
	p.routes[to] = r
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host // no port in the Host header
	}
	p.mu.RLock()
	rt, ok := p.routes[strings.ToLower(host)]
	p.mu.RUnlock()
	if !ok {
		http.Error(w, "no app is configured for this domain", http.StatusNotFound)
		return
	}
	rt.proxy.ServeHTTP(w, r)
}
