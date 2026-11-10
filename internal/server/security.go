package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Browser-facing protections. clirouter usually listens on 127.0.0.1, which a
// web page in the user's browser can still reach: by CSRF (a cross-site
// form/fetch) or by DNS rebinding (attacker.example resolving to 127.0.0.1,
// making the page "same-origin" with clirouter). These checks close both.

// isLoopbackHost reports whether a Host header (or host[:port]) names the
// local machine: localhost, *.localhost, 127.0.0.0/8 or ::1.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return false
	}
	lh := strings.ToLower(host)
	if lh == "localhost" || strings.HasSuffix(lh, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackListen reports whether a listen address only accepts local
// connections (":8317" and "0.0.0.0:8317" do not).
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return host != "" && isLoopbackHost(host)
}

// forwarded reports whether a reverse proxy relayed the request. Behind a
// proxy on the same host every request arrives from 127.0.0.1, so loopback
// alone must not grant password-less admin access.
func forwarded(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// localRequest is the bar for password-less access: loopback peer, a Host
// header naming this machine (defeats DNS rebinding) and no proxy headers.
func localRequest(r *http.Request) bool {
	return isLoopback(r) && isLoopbackHost(r.Host) && !forwarded(r)
}

// crossOrigin reports whether a browser sent the request from another
// origin. Non-browser clients (agents, curl) send neither header.
func crossOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default: // same-site, cross-site
		return true
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return true // includes the opaque "null" origin
	}
	return !strings.EqualFold(u.Host, r.Host)
}

// failLimiter throttles credential guessing per client IP: after max
// failures inside window, further attempts are refused until it expires.
type failLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*failCount
	now    func() time.Time
}

type failCount struct {
	n     int
	reset time.Time
}

func newFailLimiter(max int, window time.Duration) *failLimiter {
	return &failLimiter{max: max, window: window, m: map[string]*failCount{}, now: time.Now}
}

// clientIP is the address failures are counted against. Behind a reverse
// proxy on this host (the recommended way to expose clirouter) every request
// arrives from loopback, so throttling the peer address would let one client
// sending bad credentials lock out everyone. For loopback peers the address
// reported by the proxy is used instead: the last X-Forwarded-For hop (the
// one the proxy itself appended) or X-Real-IP.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return host
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		last := xff[len(xff)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		if ip := net.ParseIP(strings.TrimSpace(last)); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-Ip"))); ip != nil {
		return ip.String()
	}
	return host
}

// blocked reports whether ip has used up its failures for this window.
func (l *failLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.m[ip]
	if c == nil {
		return false
	}
	if l.now().After(c.reset) {
		delete(l.m, ip)
		return false
	}
	return c.n >= l.max
}

func (l *failLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) >= 10000 { // bound memory under a spray of source IPs
		for k, c := range l.m {
			if now.After(c.reset) {
				delete(l.m, k)
			}
		}
		if len(l.m) >= 10000 {
			clear(l.m)
		}
	}
	c := l.m[ip]
	if c == nil || now.After(c.reset) {
		c = &failCount{reset: now.Add(l.window)}
		l.m[ip] = c
	}
	c.n++
}

// securityHeaders hardens every response: no MIME sniffing, no framing
// (clickjacking of the dashboard), no referrer leaks.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/v1") && !strings.HasPrefix(r.URL.Path, "/chat") && !strings.HasPrefix(r.URL.Path, "/responses") {
			h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

// IsLoopbackListen reports whether a listen address only accepts local
// connections.
func IsLoopbackListen(addr string) bool { return isLoopbackListen(addr) }
