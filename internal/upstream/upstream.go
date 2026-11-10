// Package upstream owns outbound HTTP: pooled transports (one per proxy URL,
// HTTP/2 when the server offers it) and per-provider request construction.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/router"
)

// Native lists the wire formats a provider type speaks, in preference order
// for translation.
func Native(providerType string) []ir.Format {
	switch providerType {
	case "anthropic", "claude-oauth":
		return []ir.Format{ir.FormatAnthropic}
	case "openai":
		return []ir.Format{ir.FormatChat, ir.FormatResponses}
	case "codex":
		return []ir.Format{ir.FormatResponses}
	case "gemini", "vertex", "gemini-cli":
		return []ir.Format{ir.FormatGemini}
	default: // openai-compatible, copilot
		return []ir.Format{ir.FormatChat}
	}
}

// Choose returns the upstream format and whether the body can be forwarded
// without translation.
func Choose(providerType string, client ir.Format) (ir.Format, bool) {
	n := Native(providerType)
	for _, f := range n {
		if f == client {
			return f, true
		}
	}
	return n[0], false
}

type Clients struct {
	mu      sync.Mutex
	connect time.Duration
	byProxy map[string]*http.Client
}

func NewClients(connect time.Duration) *Clients {
	return &Clients{connect: connect, byProxy: map[string]*http.Client{}}
}

func (c *Clients) Get(proxy string) (*http.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.byProxy[proxy]; ok {
		return cl, nil
	}
	d := &net.Dialer{Timeout: c.connect, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   c.connect,
		ExpectContinueTimeout: time.Second,
		// Never negotiate gzip: compressed SSE is flushed in blocks and adds latency.
		DisableCompression: true,
		ReadBufferSize:     64 << 10,
		WriteBufferSize:    64 << 10,
	}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy %q: %w", proxy, err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.byProxy[proxy] = cl
	return cl, nil
}

// Close drops idle connections of every pooled transport.
func (c *Clients) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cl := range c.byProxy {
		cl.CloseIdleConnections()
	}
}

// Client headers that are safe and useful to forward per format.
var forwardHeaders = map[ir.Format][]string{
	ir.FormatAnthropic: {"Anthropic-Beta", "Anthropic-Version"},
	ir.FormatResponses: {"Openai-Beta"},
}

// Build constructs the upstream request.
func Build(ctx context.Context, a *router.Account, f ir.Format, model string, body []byte, stream bool, in http.Header) (*http.Request, error) {
	p := a.Provider.Cfg
	base := p.BaseURL
	var u string
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if stream {
		h.Set("Accept", "text/event-stream")
	} else {
		h.Set("Accept", "application/json")
	}
	h.Set("User-Agent", "clirouter/0.1.0")

	token := a.APIKey()
	var meta map[string]string
	if a.OAuth != nil {
		t, err := a.OAuth.Token()
		if err != nil {
			return nil, err
		}
		token = t
		meta = a.OAuth.Meta()
		if b := meta["base_url"]; b != "" {
			base = strings.TrimRight(b, "/")
		}
	}

	switch p.Type {
	case "anthropic", "claude-oauth":
		u = base + "/v1/messages"
		h.Set("Anthropic-Version", "2023-06-01")
		if p.Type == "claude-oauth" {
			h.Set("Authorization", "Bearer "+token)
		} else {
			h.Set("X-Api-Key", token)
		}
	case "gemini":
		u = geminiURL(base+"/v1beta/models/", model, stream)
		h.Set("X-Goog-Api-Key", token)
	case "gemini-cli":
		// Google Code Assist wraps the generateContent body.
		u = base + "/v1internal:generateContent"
		if stream {
			u = base + "/v1internal:streamGenerateContent?alt=sse"
		}
		h.Set("Authorization", "Bearer "+token)
		wrapped, err := json.Marshal(map[string]any{"model": model, "project": meta["project"], "request": json.RawMessage(body)})
		if err != nil {
			return nil, err
		}
		body = wrapped
	case "codex":
		u = base + "/responses"
		h.Set("Authorization", "Bearer "+token)
		h.Set("Openai-Beta", "responses=experimental")
	case "vertex":
		u = geminiURL(base+"/v1/publishers/google/models/", model, stream)
		if a.OAuth != nil {
			h.Set("Authorization", "Bearer "+token)
		} else {
			h.Set("X-Goog-Api-Key", token)
		}
	default:
		if f == ir.FormatResponses {
			u = base + "/responses"
		} else {
			u = base + "/chat/completions"
		}
		h.Set("Authorization", "Bearer "+token)
	}
	for _, k := range forwardHeaders[f] {
		if v := in.Values(k); len(v) > 0 {
			h[k] = v
		}
	}
	if p.Type == "claude-oauth" {
		addBeta(h, "oauth-2025-04-20")
	}
	if meta != nil {
		for k, v := range meta {
			if hk, ok := strings.CutPrefix(k, "header:"); ok {
				h.Set(hk, v)
			}
		}
	}
	for k, v := range p.Headers {
		h.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = h
	req.ContentLength = int64(len(body))
	return req, nil
}

func geminiURL(prefix, model string, stream bool) string {
	if stream {
		return prefix + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
	}
	return prefix + url.PathEscape(model) + ":generateContent"
}

func addBeta(h http.Header, v string) {
	cur := h.Get("Anthropic-Beta")
	for _, b := range strings.Split(cur, ",") {
		if strings.TrimSpace(b) == v {
			return
		}
	}
	if cur == "" {
		h.Set("Anthropic-Beta", v)
	} else {
		h.Set("Anthropic-Beta", cur+","+v)
	}
}
