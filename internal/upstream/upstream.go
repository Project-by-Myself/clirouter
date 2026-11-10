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

	"github.com/tidwall/gjson"

	"github.com/Project-by-Myself/clirouter/internal/cloudauth"
	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/router"
)

var (
	fmtAnthropic = []ir.Format{ir.FormatAnthropic}
	fmtOpenAI    = []ir.Format{ir.FormatChat, ir.FormatResponses}
	fmtResponses = []ir.Format{ir.FormatResponses}
	fmtGemini    = []ir.Format{ir.FormatGemini}
	fmtChat      = []ir.Format{ir.FormatChat}
)

// Native lists the wire formats a provider type speaks for the upstream
// model, in preference order for translation. Only vertex depends on the
// model: Claude models on Vertex speak Anthropic Messages, the rest Gemini.
func Native(providerType, model string) []ir.Format {
	switch providerType {
	case "anthropic", "claude-oauth", "bedrock":
		return fmtAnthropic
	case "openai", "azure-openai":
		return fmtOpenAI
	case "codex":
		return fmtResponses
	case "vertex":
		if IsClaudeModel(model) {
			return fmtAnthropic
		}
		return fmtGemini
	case "gemini", "gemini-cli":
		return fmtGemini
	default: // openai-compatible, copilot, ollama
		return fmtChat
	}
}

// IsClaudeModel reports an Anthropic model id ("claude-…", or a publisher
// path such as "anthropic/claude-…").
func IsClaudeModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "claude") || strings.HasPrefix(m, "anthropic/") || strings.HasPrefix(m, "anthropic.")
}

// Choose returns the upstream format and whether the body can be forwarded
// without translation.
func Choose(providerType, model string, client ir.Format) (ir.Format, bool) {
	n := Native(providerType, model)
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

// Version tags that Anthropic models on cloud platforms expect in the body
// (instead of the Anthropic-Version header).
const (
	BedrockAnthropicVersion = "bedrock-2023-05-31"
	VertexAnthropicVersion  = "vertex-2023-10-16"
)

// now is the signing clock (tests pin it).
var now = time.Now

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

	fwd := forwardHeaders[f]
	var aws *cloudauth.AWSCredentials
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
		loc := p.VertexLocation()
		project := p.Project
		if project == "" {
			project = meta["project"]
		}
		if project != "" {
			// Regional locations are served by "<loc>-aiplatform.googleapis.com".
			if base == config.DefaultBaseURL["vertex"] && loc != "global" {
				base = "https://" + loc + "-aiplatform.googleapis.com"
			}
			base += "/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(loc)
		} else if f == ir.FormatAnthropic {
			return nil, fmt.Errorf("vertex: Claude models need a Google Cloud project (use a service account key or set project)")
		}
		if f == ir.FormatAnthropic {
			u = base + "/publishers/anthropic/models/" + url.PathEscape(model) + ":rawPredict"
			if stream {
				u = base + "/publishers/anthropic/models/" + url.PathEscape(model) + ":streamRawPredict"
			}
			body = cloudAnthropicBody(body, VertexAnthropicVersion, true)
			fwd = vertexAnthropicHeaders
		} else if project != "" {
			u = geminiURL(base+"/publishers/google/models/", model, stream)
		} else {
			// Vertex AI express mode (API key, no project).
			u = geminiURL(base+"/v1/publishers/google/models/", model, stream)
		}
		if a.OAuth != nil {
			h.Set("Authorization", "Bearer "+token)
		} else {
			h.Set("X-Goog-Api-Key", token)
		}
	case "bedrock":
		// Bedrock takes the model id in the path; ':' ("...-v1:0") must be
		// escaped, which url.PathEscape leaves alone.
		u = base + "/model/" + cloudauth.URIEncode(model) + "/invoke"
		if stream {
			u += "-with-response-stream"
			h.Set("Accept", EventStreamType)
		}
		body = cloudAnthropicBody(body, BedrockAnthropicVersion, false)
		// The beta/version headers of the Anthropic API are not accepted
		// by Bedrock (the version travels in the body).
		fwd = nil
		creds, isAWS, err := cloudauth.ParseAWSKey(token)
		switch {
		case err != nil:
			return nil, err
		case isAWS:
			aws = &creds
		default: // Bedrock API key
			h.Set("Authorization", "Bearer "+token)
		}
	case "azure-openai":
		// Azure OpenAI v1 API: OpenAI-compatible bodies, the model field
		// names the deployment.
		if !strings.HasSuffix(base, "/openai/v1") {
			base = strings.TrimSuffix(base, "/openai") + "/openai/v1"
		}
		if f == ir.FormatResponses {
			u = base + "/responses"
		} else {
			u = base + "/chat/completions"
		}
		h.Set("Api-Key", token)
	default:
		if f == ir.FormatResponses {
			u = base + "/responses"
		} else {
			u = base + "/chat/completions"
		}
		if token != "" { // ollama runs without a key
			h.Set("Authorization", "Bearer "+token)
		}
	}
	for _, k := range fwd {
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
	if aws != nil {
		cloudauth.SignV4(req, body, *aws, p.BedrockRegion(), "bedrock", now())
	}
	return req, nil
}

var vertexAnthropicHeaders = []string{"Anthropic-Beta"}

// cloudAnthropicBody rewrites an Anthropic Messages body for Bedrock or
// Vertex: "model" is dropped (it is in the URL), "anthropic_version" is
// set, and "stream" is dropped unless keepStream (Bedrock picks streaming
// by endpoint, Vertex reads the flag). One pass, one allocation.
func cloudAnthropicBody(body []byte, version string, keepStream bool) []byte {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body
	}
	out := make([]byte, 0, len(body)+len(version)+24)
	out = append(out, `{"anthropic_version":"`...)
	out = append(out, version...)
	out = append(out, '"')
	root.ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "model", "anthropic_version":
			return true
		case "stream":
			if !keepStream {
				return true
			}
		}
		out = append(out, ',')
		out = append(out, k.Raw...)
		out = append(out, ':')
		out = append(out, v.Raw...)
		return true
	})
	return append(out, '}')
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
