package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/router"
	"github.com/Project-by-Myself/clirouter/internal/upstream"
)

const anthropicOK = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const anthropicReq = `{"model":"claude-x","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

type mock struct {
	*httptest.Server
	hits atomic.Int64
}

func newMock(t *testing.T, h func(n int64, w http.ResponseWriter, r *http.Request)) *mock {
	m := &mock{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(m.hits.Add(1), w, r)
	}))
	t.Cleanup(m.Close)
	return m
}

func sseWrite(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, s)
	w.(http.Flusher).Flush()
}

func testCfg(providers ...config.Provider) *config.Config {
	cfg := config.Default()
	cfg.Timeouts.FirstByte = config.Duration(400 * time.Millisecond)
	cfg.Timeouts.Idle = config.Duration(400 * time.Millisecond)
	cfg.Timeouts.EarlyPing = config.Duration(100 * time.Millisecond)
	cfg.Timeouts.Keepalive = config.Duration(100 * time.Millisecond)
	cfg.Providers = providers
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func front(t *testing.T, cfg *config.Config) (*httptest.Server, *Handler) {
	rt := &Runtime{Cfg: cfg, Router: router.New(cfg, nil, nil), Clients: upstream.NewClients(time.Second)}
	h := NewHandler(rt, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, ir.FormatAnthropic, "", false, "t") })
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, ir.FormatChat, "", false, "t") })
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, ir.FormatResponses, "", false, "t") })
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, h
}

func post(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func anthropicProvider(id, url string, keys ...string) config.Provider {
	return config.Provider{ID: id, Type: "anthropic", BaseURL: url, Keys: keys}
}

func TestPassthroughIsByteExact(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k1" || r.URL.Path != "/v1/messages" {
			http.Error(w, "bad", 400)
			return
		}
		sseWrite(w, anthropicOK)
	})
	s, h := front(t, testCfg(anthropicProvider("a", up.URL, "k1")))
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || body != anthropicOK {
		t.Fatalf("status %d body:\n%q", resp.StatusCode, body)
	}
	if h.Metrics.Snapshot().OutputTokens != 5 || h.Metrics.Snapshot().InputTokens != 10 {
		t.Fatalf("usage not recorded: %+v", h.Metrics.Snapshot())
	}
}

func TestRateLimitFailsOverToNextAccount(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") == "k1" {
			w.Header().Set("Retry-After", "30")
			http.Error(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, 429)
			return
		}
		sseWrite(w, anthropicOK)
	})
	cfg := testCfg(anthropicProvider("a", up.URL, "k1", "k2"))
	cfg.Routing.Strategy = "fill-first"
	s, h := front(t, cfg)
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || body != anthropicOK {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	// k1 must now be cooling down for this model; next request goes straight to k2.
	before := up.hits.Load()
	post(t, s.URL+"/v1/messages", anthropicReq)
	if got := up.hits.Load() - before; got != 1 {
		t.Fatalf("expected 1 upstream hit after cooldown, got %d", got)
	}
	st := h.Runtime().Router.Providers()[0].Accounts[0].Status()
	if st.ModelCooldown["claude-x"] == "" {
		t.Fatalf("k1 not cooling: %+v", st)
	}
}

func TestFirstByteTimeoutFailsOverToNextProvider(t *testing.T) {
	slow := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	fast := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { sseWrite(w, anthropicOK) })
	p1 := anthropicProvider("slow", slow.URL, "k")
	p2 := anthropicProvider("fast", fast.URL, "k")
	p2.Priority = 1
	p1.Models, p2.Models = []string{"claude-x"}, []string{"claude-x"}
	s, _ := front(t, testCfg(p1, p2))
	start := time.Now()
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || !strings.Contains(body, "Hello") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	// Pings were sent while waiting, so the body starts with one.
	if !strings.HasPrefix(body, "event: ping") {
		t.Fatalf("expected early keepalive ping, got %q", body[:min(40, len(body))])
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("failover too slow: %v", d)
	}
}

func TestInbandOverloadBeforeContentFailsOver(t *testing.T) {
	bad := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		sseWrite(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	})
	good := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { sseWrite(w, anthropicOK) })
	p1, p2 := anthropicProvider("bad", bad.URL, "k"), anthropicProvider("good", good.URL, "k")
	p1.Models, p2.Models, p2.Priority = []string{"claude-x"}, []string{"claude-x"}, 1
	s, _ := front(t, testCfg(p1, p2))
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || strings.Contains(body, "Overloaded") || !strings.Contains(body, "Hello") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if strings.Count(body, "event: message_start") != 1 {
		t.Fatalf("held preamble leaked: %q", body)
	}
}

func TestMidStreamStallEndsWithInbandError(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitAfter(anthropicOK, "\n\n")
		sseWrite(w, strings.Join(parts[:3], ""))
		<-r.Context().Done() // stall forever after some content
	})
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k")))
	start := time.Now()
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || !strings.Contains(body, "Hello") || !strings.Contains(body, "event: error") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stall not detected promptly: %v", d)
	}
}

func TestClientDisconnectCancelsUpstream(t *testing.T) {
	canceled := make(chan struct{})
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		sseWrite(w, strings.SplitAfter(anthropicOK, "\n\n")[0])
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				close(canceled)
				return
			case <-time.After(20 * time.Millisecond):
				sseWrite(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
			}
		}
	})
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k")))
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", s.URL+"/v1/messages", strings.NewReader(anthropicReq))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	br.ReadString('\n')
	cancel()
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream was not canceled after client disconnect")
	}
}

func TestChatClientToAnthropicUpstream(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { sseWrite(w, anthropicOK) })
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k")))
	resp, body := post(t, s.URL+"/v1/chat/completions", `{"model":"claude-x","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"content":"Hello"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") || !strings.Contains(body, `"completion_tokens":5`) {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	// non-stream
	resp, body = post(t, s.URL+"/v1/chat/completions", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &out) != nil || len(out.Choices) == 0 || out.Choices[0].Message.Content != "Hello" {
		t.Fatalf("non-stream status %d body %q", resp.StatusCode, body)
	}
}

func TestResponsesClientToAnthropicUpstream(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { sseWrite(w, anthropicOK) })
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k")))
	resp, body := post(t, s.URL+"/v1/responses", `{"model":"claude-x","stream":true,"input":"hi"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func TestFatalErrorPassesThroughUpstreamBody(t *testing.T) {
	const errBody = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { http.Error(w, errBody, 400) })
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k1", "k2")))
	resp, body := post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, `"stream":true`, `"stream":false`, 1))
	if resp.StatusCode != 400 || !strings.Contains(body, "prompt is too long") || up.hits.Load() != 1 {
		t.Fatalf("status %d hits %d body %q", resp.StatusCode, up.hits.Load(), body)
	}
}

func TestAllCoolingReturns429WithRetryAfter(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"error":{"message":"limited"}}`, 429)
	})
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k")))
	resp, _ := post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, `"stream":true`, `"stream":false`, 1))
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("first: status %d ra %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	resp, _ = post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, `"stream":true`, `"stream":false`, 1))
	if resp.StatusCode != 429 || up.hits.Load() != 1 {
		t.Fatalf("second: status %d hits %d", resp.StatusCode, up.hits.Load())
	}
}

func TestShortCooldownIsWaitedOut(t *testing.T) {
	up := newMock(t, func(n int64, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":{"message":"limited"}}`, 429)
			return
		}
		sseWrite(w, anthropicOK)
	})
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k")))
	post(t, s.URL+"/v1/messages", anthropicReq) // trips the 1s cooldown
	resp, body := post(t, s.URL+"/v1/messages", anthropicReq)
	if resp.StatusCode != 200 || !strings.Contains(body, "Hello") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func BenchmarkPassthroughOverhead(b *testing.B) {
	var stream strings.Builder
	stream.WriteString(strings.Join(strings.SplitAfter(anthropicOK, "\n\n")[:3], ""))
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&stream, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"token %d \"}}\n\n", i)
	}
	stream.WriteString(strings.Join(strings.SplitAfter(anthropicOK, "\n\n")[3:], ""))
	payload := stream.String()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sseWrite(w, payload) }))
	defer up.Close()
	cfg := testCfg(anthropicProvider("a", up.URL, "k"))
	rt := &Runtime{Cfg: cfg, Router: router.New(cfg, nil, nil), Clients: upstream.NewClients(time.Second)}
	h := NewHandler(rt, nil)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, ir.FormatAnthropic, "", false, "") }))
	defer s.Close()
	direct := func(url string) {
		resp, err := http.Post(url, "application/json", strings.NewReader(anthropicReq))
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	b.Run("direct", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			direct(up.URL + "/v1/messages")
		}
	})
	b.Run("via-proxy", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			direct(s.URL + "/v1/messages")
		}
	})
}
