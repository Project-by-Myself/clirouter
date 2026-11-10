package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

// A combo that falls back to another model on the same account must still be
// tried after the first model is rate limited (or missing): the failure is
// scoped to the model, not to the account.
func TestComboFallsBackToOtherModelOnSameAccount(t *testing.T) {
	for _, status := range []int{429, 404} {
		up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), `"model":"big"`) {
				w.Header().Set("Retry-After", "30")
				http.Error(w, `{"type":"error","error":{"type":"x","message":"nope"}}`, status)
				return
			}
			sseWrite(w, anthropicOK)
		})
		cfg := testCfg(anthropicProvider("a", up.URL, "k1"))
		cfg.Combos = []config.Combo{{Name: "c", Strategy: "fallback", Targets: []string{"a/big", "a/small"}}}
		s, _ := front(t, cfg)
		resp, body := post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, "claude-x", "c", 1))
		if resp.StatusCode != 200 || !strings.Contains(body, "Hello") || up.hits.Load() != 2 {
			t.Fatalf("status %d after upstream %d: hits %d body %q", resp.StatusCode, status, up.hits.Load(), body)
		}
	}
}

// An auth failure rules the account out for every model of the combo.
func TestAuthFailureExcludesAccountForAllModels(t *testing.T) {
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, 401)
	})
	cfg := testCfg(anthropicProvider("a", up.URL, "k1"))
	cfg.Combos = []config.Combo{{Name: "c", Strategy: "fallback", Targets: []string{"a/big", "a/small"}}}
	s, _ := front(t, cfg)
	resp, _ := post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, "claude-x", "c", 1))
	if resp.StatusCode != 401 || up.hits.Load() != 1 {
		t.Fatalf("status %d hits %d", resp.StatusCode, up.hits.Load())
	}
}

// A non-streaming upstream sends its headers only when the completion is
// done; the first-byte timeout must not cut (and re-run) such a request.
func TestNonStreamPassthroughNotCutByFirstByte(t *testing.T) {
	old := nonStreamFirstByte
	nonStreamFirstByte = 2 * time.Second
	t.Cleanup(func() { nonStreamFirstByte = old })
	const msg = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond) // > first_byte (400ms)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, msg)
	})
	s, _ := front(t, testCfg(anthropicProvider("a", up.URL, "k1", "k2")))
	resp, body := post(t, s.URL+"/v1/messages", strings.Replace(anthropicReq, `"stream":true`, `"stream":false`, 1))
	if resp.StatusCode != 200 || body != msg || up.hits.Load() != 1 {
		t.Fatalf("status %d hits %d body %q", resp.StatusCode, up.hits.Load(), body)
	}
}

// Some OpenAI-compatible servers end the stream after the finish_reason chunk
// without "data: [DONE]"; that is a complete response, not an error.
func TestChatPassthroughWithoutDoneMarker(t *testing.T) {
	const chunks = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason": "stop"}]}` + "\n\n"
	up := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) { sseWrite(w, chunks) })
	p := config.Provider{ID: "o", Type: "openai-compatible", BaseURL: up.URL, Keys: []string{"k"}, Models: []string{"m"}}
	s, h := front(t, testCfg(p))
	resp, body := post(t, s.URL+"/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || body != chunks+"data: [DONE]\n\n" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if st := h.Runtime().Router.Providers()[0].Accounts[0].Status(); st.Failures != 0 || st.Successes != 1 {
		t.Fatalf("account marked failed: %+v", st)
	}
	// Without a finish_reason, an early close is still an error.
	up2 := newMock(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		sseWrite(w, strings.SplitAfter(chunks, "\n\n")[0])
	})
	p.BaseURL = up2.URL
	s2, _ := front(t, testCfg(p))
	_, body = post(t, s2.URL+"/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, "upstream stream ended unexpectedly") {
		t.Fatalf("truncated stream not reported: %q", body)
	}
}
