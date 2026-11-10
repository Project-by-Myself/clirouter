package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/store"
)

// mockAnthropic answers every request with 1000 input / 500 output tokens
// and Anthropic rate-limit headers.
func mockAnthropic(t *testing.T, hits *atomic.Int64) *httptest.Server {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("anthropic-ratelimit-requests-limit", "100")
		w.Header().Set("anthropic-ratelimit-requests-remaining", "40")
		w.Header().Set("anthropic-ratelimit-requests-reset", time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.25")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":500}}`)
	}))
	t.Cleanup(up.Close)
	return up
}

func usageServer(t *testing.T, dir string, cfg *config.Config) (*Server, *httptest.Server, *store.Store) {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	cfg.DataDir = dir
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir, 14)
	if err != nil {
		t.Fatal(err)
	}
	s := New(path, cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, hs, st
}

func msg(t *testing.T, base, key, model string) (int, string) {
	c, body, _ := do(t, "POST", base+"/v1/messages", `{"model":"`+model+`","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
		"x-api-key", key, "anthropic-version", "2023-06-01", "Content-Type", "application/json")
	return c, body
}

const sonnetCost = (1000*3 + 500*15) / 1e6 // claude-sonnet-4-5 list price

func TestKeyPoliciesUsageAndQuota(t *testing.T) {
	var hits atomic.Int64
	up := mockAnthropic(t, &hits)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.APIKeys = []config.ClientKey{
		{Name: "alice", Key: "sk-alice", Models: []string{"claude-sonnet-*"}, BudgetUSD: 0.02},
		{Name: "bob", Key: "sk-bob", RPM: 1},
		{Name: "carol", Key: "sk-carol", Disabled: true},
		{Name: "dave", Key: "sk-dave", ExpiresAt: "2020-01-01T00:00:00Z"},
	}
	cfg.Providers = []config.Provider{{ID: "ant", Type: "anthropic", BaseURL: up.URL, Keys: []string{"sk-up"}, Models: []string{"claude-sonnet-4-5", "claude-haiku-4-5"}}}
	s, hs, st := usageServer(t, dir, cfg)

	// Allowed model list.
	if c, body := msg(t, hs.URL, "sk-alice", "claude-haiku-4-5"); c != http.StatusForbidden || !strings.Contains(body, "not allowed") {
		t.Fatalf("disallowed model: %d %s", c, body)
	}
	_, models := get(t, hs.URL+"/v1/models", "Authorization", "Bearer sk-alice")
	if strings.Contains(models, "claude-haiku-4-5") || !strings.Contains(models, "claude-sonnet-4-5") {
		t.Fatalf("/v1/models not filtered: %s", models)
	}
	// Budget: two requests fit under $0.02, the second crosses it, the third is refused.
	for i := 0; i < 2; i++ {
		if c, body := msg(t, hs.URL, "sk-alice", "claude-sonnet-4-5"); c != 200 {
			t.Fatalf("request %d: %d %s", i, c, body)
		}
	}
	if c, body := msg(t, hs.URL, "sk-alice", "claude-sonnet-4-5"); c != http.StatusPaymentRequired || !strings.Contains(body, "budget") {
		t.Fatalf("over budget: %d %s", c, body)
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits = %d, want 2 (refused requests must not reach upstream)", hits.Load())
	}
	// RPM.
	if c, _ := msg(t, hs.URL, "sk-bob", "claude-sonnet-4-5"); c != 200 {
		t.Fatalf("bob first: %d", c)
	}
	c, body, hdr := do(t, "POST", hs.URL+"/v1/messages", `{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "x-api-key", "sk-bob", "anthropic-version", "2023-06-01")
	if c != http.StatusTooManyRequests || hdr.Get("Retry-After") == "" {
		t.Fatalf("rpm: %d %s %v", c, body, hdr)
	}
	// Disabled / expired.
	if c, _ := msg(t, hs.URL, "sk-carol", "claude-sonnet-4-5"); c != http.StatusUnauthorized {
		t.Fatalf("disabled: %d", c)
	}
	if c, body := msg(t, hs.URL, "sk-dave", "claude-sonnet-4-5"); c != http.StatusUnauthorized || !strings.Contains(body, "expired") {
		t.Fatalf("expired: %d %s", c, body)
	}

	// Self-service info.
	c, info := get(t, hs.URL+"/v1/key/info", "Authorization", "Bearer sk-alice")
	var ki struct {
		Name   string
		Status string
		Models []string
		Usage  struct {
			Requests int64
			Cost     float64 `json:"cost_usd"`
		}
	}
	if c != 200 || json.Unmarshal([]byte(info), &ki) != nil {
		t.Fatalf("key info: %d %s", c, info)
	}
	if ki.Name != "alice" || ki.Status != "over_budget" || ki.Usage.Requests != 2 || math.Abs(ki.Usage.Cost-2*sonnetCost) > 1e-9 {
		t.Fatalf("key info: %+v", ki)
	}

	// Upstream quota windows on the account.
	_, accts := get(t, hs.URL+"/admin/api/accounts")
	if !strings.Contains(accts, `"name":"requests"`) || !strings.Contains(accts, `"remaining":40`) || !strings.Contains(accts, `"name":"5h"`) {
		t.Fatalf("quota not reported: %s", accts)
	}

	// The rollup survives a restart and seeds the tracker.
	time.Sleep(600 * time.Millisecond) // writer flushes every 250ms
	_, rep := get(t, hs.URL+"/admin/api/usage?range=7d&group=key")
	if !strings.Contains(rep, `"key":"alice"`) {
		t.Fatalf("usage report: %s", rep)
	}
	_, ku := get(t, hs.URL+"/admin/api/keys/usage")
	if !strings.Contains(ku, `"name":"alice","status":"over_budget"`) {
		t.Fatalf("keys usage: %s", ku)
	}
	hs.Close()
	st.Close()
	_ = s
	cfg2, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, hs2, st2 := usageServer(t, dir, cfg2)
	defer st2.Close()
	if c, _ := msg(t, hs2.URL, "sk-alice", "claude-sonnet-4-5"); c != http.StatusPaymentRequired {
		t.Fatalf("budget forgotten after restart: %d", c)
	}
}

func TestProviderBudgetFallsBack(t *testing.T) {
	var h1, h2 atomic.Int64
	up1, up2 := mockAnthropic(t, &h1), mockAnthropic(t, &h2)
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{ID: "cheap", Type: "anthropic", BaseURL: up1.URL, Keys: []string{"k1"}, Models: []string{"claude-sonnet-4-5"}, Priority: 1, BudgetUSD: 0.01, BudgetPeriod: "daily"},
		{ID: "backup", Type: "anthropic", BaseURL: up2.URL, Keys: []string{"k2"}, Models: []string{"claude-sonnet-4-5"}, Priority: 2},
	}
	_, hs, st := usageServer(t, t.TempDir(), cfg)
	defer st.Close()
	for i := 0; i < 3; i++ {
		if c, body := msg(t, hs.URL, "", "claude-sonnet-4-5"); c != 200 {
			t.Fatalf("request %d: %d %s", i, c, body)
		}
	}
	if h1.Load() != 1 || h2.Load() != 2 {
		t.Fatalf("hits cheap=%d backup=%d, want 1 and 2", h1.Load(), h2.Load())
	}
	_, pu := get(t, hs.URL+"/admin/api/providers/usage")
	if !strings.Contains(pu, `"id":"cheap","over_budget":true`) {
		t.Fatalf("providers usage: %s", pu)
	}
}

func TestPricingOverridesAndCostInStats(t *testing.T) {
	var hits atomic.Int64
	up := mockAnthropic(t, &hits)
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "mm", Type: "anthropic", BaseURL: up.URL, Keys: []string{"k"}, Models: []string{"claude-sonnet-4-5"}}}
	cfg.Pricing = map[string]config.Price{"mm/*": {Input: 0, Output: 0}} // flat-rate plan
	_, hs, st := usageServer(t, t.TempDir(), cfg)
	defer st.Close()
	if c, _ := msg(t, hs.URL, "", "claude-sonnet-4-5"); c != 200 {
		t.Fatal(c)
	}
	time.Sleep(600 * time.Millisecond)
	_, stats := get(t, hs.URL+"/admin/api/stats?range=1h")
	if !strings.Contains(stats, `"cost_usd":0`) {
		t.Fatalf("override not applied: %s", stats)
	}
	_, pr := get(t, hs.URL+"/admin/api/pricing")
	if !strings.Contains(pr, `"claude-sonnet-4-5"`) || !strings.Contains(pr, `"mm/*"`) {
		t.Fatalf("pricing: %s", pr)
	}
}
