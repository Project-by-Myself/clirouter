package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/pricing"
	"github.com/Project-by-Myself/clirouter/internal/router"
)

func TestKeyManagementAPI(t *testing.T) {
	var hits atomic.Int64
	up := mockAnthropic(t, &hits)
	cfg := config.Default()
	cfg.APIKeys = []config.ClientKey{{Name: "admin", Key: "sk-admin-key-000000"}}
	cfg.Providers = []config.Provider{{ID: "ant", Type: "anthropic", BaseURL: up.URL, Keys: []string{"k"}, Models: []string{"claude-sonnet-4-5"}}}
	s, hs, st := usageServer(t, t.TempDir(), cfg)
	defer st.Close()

	c, body, _ := do(t, "POST", hs.URL+"/admin/api/keys", `{"name":"ci","models":["claude-*"],"budget_usd":5,"tpm":1000,"expires_in":"30d","note":"CI"}`)
	var created struct {
		Key       string `json:"key"`
		Name      string
		Hint      string `json:"key_hint"`
		ExpiresAt string `json:"expires_at"`
		TPM       int64
	}
	if c != http.StatusCreated || json.Unmarshal([]byte(body), &created) != nil || !strings.HasPrefix(created.Key, "sk-clr-") || created.TPM != 1000 || created.ExpiresAt == "" {
		t.Fatalf("create: %d %s", c, body)
	}
	if strings.Contains(body, "sha256:") {
		t.Fatalf("stored hash leaked: %s", body)
	}
	if c, _, _ := do(t, "POST", hs.URL+"/admin/api/keys", `{"name":"ci"}`); c != http.StatusConflict {
		t.Fatalf("duplicate name: %d", c)
	}
	if c, _, _ := do(t, "POST", hs.URL+"/admin/api/keys", `{"name":"x","bogus":1}`); c != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", c)
	}
	// The new key works; TPM (1000) is exceeded after one 1500-token request.
	if c, b := msg(t, hs.URL, created.Key, "claude-sonnet-4-5"); c != 200 {
		t.Fatalf("new key: %d %s", c, b)
	}
	if c, b := msg(t, hs.URL, created.Key, "claude-sonnet-4-5"); c != http.StatusTooManyRequests || !strings.Contains(b, "tokens per minute") {
		t.Fatalf("tpm: %d %s", c, b)
	}
	// Update, rotate, delete; other keys untouched.
	if c, b, _ := do(t, "PATCH", hs.URL+"/admin/api/keys/ci", `{"tpm":0,"disabled":true}`); c != 200 || !strings.Contains(b, `"status":"disabled"`) {
		t.Fatalf("patch: %d %s", c, b)
	}
	if c, b := msg(t, hs.URL, created.Key, "claude-sonnet-4-5"); c != http.StatusUnauthorized {
		t.Fatalf("disabled key: %d %s", c, b)
	}
	do(t, "PATCH", hs.URL+"/admin/api/keys/ci", `{"disabled":false}`)
	c, body, _ = do(t, "POST", hs.URL+"/admin/api/keys/ci/rotate", "")
	var rot struct{ Key string }
	if c != 200 || json.Unmarshal([]byte(body), &rot) != nil || rot.Key == "" || rot.Key == created.Key {
		t.Fatalf("rotate: %d %s", c, body)
	}
	if c, _ := msg(t, hs.URL, created.Key, "claude-sonnet-4-5"); c != http.StatusUnauthorized {
		t.Fatalf("old key still works after rotate: %d", c)
	}
	if c, b := msg(t, hs.URL, rot.Key, "claude-sonnet-4-5"); c != 200 {
		t.Fatalf("rotated key: %d %s", c, b)
	}
	if c, _, _ := do(t, "DELETE", hs.URL+"/admin/api/keys/ci", ""); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	if c, _ := msg(t, hs.URL, rot.Key, "claude-sonnet-4-5"); c != http.StatusUnauthorized {
		t.Fatalf("deleted key works: %d", c)
	}
	if c, _ := msg(t, hs.URL, "sk-admin-key-000000", "claude-sonnet-4-5"); c != 200 {
		t.Fatalf("other key broken: %d", c)
	}
	if len(s.h.Runtime().Cfg.APIKeys) != 1 {
		t.Fatalf("keys: %+v", s.h.Runtime().Cfg.APIKeys)
	}
	_, list := get(t, hs.URL+"/admin/api/keys")
	if !strings.Contains(list, `"key_hint":"…`) || strings.Contains(list, "sk-admin-key") {
		t.Fatalf("list: %s", list)
	}
}

func TestBudgetAndTestAlerts(t *testing.T) {
	var mu sync.Mutex
	var hooks []map[string]any
	var tg []url.Values
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/botTOKEN123/sendMessage") {
			v, _ := url.ParseQuery(string(b))
			tg = append(tg, v)
			return
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		hooks = append(hooks, m)
	}))
	defer sink.Close()
	var hits atomic.Int64
	up := mockAnthropic(t, &hits)
	cfg := config.Default()
	cfg.APIKeys = []config.ClientKey{{Name: "dev", Key: "sk-dev-key-0000000", BudgetUSD: 0.025}}
	cfg.Providers = []config.Provider{{ID: "ant", Type: "anthropic", BaseURL: up.URL, Keys: []string{"k"}, Models: []string{"claude-sonnet-4-5"}}}
	cfg.Alerts = config.Alerts{WebhookURL: sink.URL + "/hook", TelegramBotToken: "TOKEN123", TelegramChatID: "42"}
	s, hs, st := usageServer(t, t.TempDir(), cfg)
	defer st.Close()
	s.h.Alerts.TelegramAPI = sink.URL
	ctx, cancel := contextWithCancel()
	defer cancel()
	go s.h.Alerts.Run(ctx)

	// $0.0105 per request against $0.025: request 2 crosses 80%, request 3 crosses 100%.
	for i := 0; i < 3; i++ {
		msg(t, hs.URL, "sk-dev-key-0000000", "claude-sonnet-4-5")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(hooks)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	if len(hooks) != 2 || hooks[0]["threshold"] != float64(80) || hooks[1]["threshold"] != float64(100) || len(tg) != 2 || tg[0].Get("chat_id") != "42" {
		t.Fatalf("hooks=%v tg=%v", hooks, tg)
	}
	mu.Unlock()
	// The masked config does not leak the token or webhook URL.
	_, cfgJSON := get(t, hs.URL+"/admin/api/config")
	if strings.Contains(cfgJSON, "TOKEN123") || strings.Contains(cfgJSON, "/hook") {
		t.Fatalf("alert secrets leaked: %s", cfgJSON)
	}
	if c, b, _ := do(t, "POST", hs.URL+"/admin/api/alerts/test", ""); c != 200 {
		t.Fatalf("test alert: %d %s", c, b)
	}
}

func TestPricingSyncEndpoint(t *testing.T) {
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"brand-new-model-x": {"input_cost_per_token": 4e-06, "output_cost_per_token": 8e-06, "litellm_provider": "openai", "mode": "chat"}}`)
	}))
	defer src.Close()
	defer pricing.SetSynced(nil)
	cfg := config.Default()
	cfg.PricingSync.URL = src.URL
	_, hs, st := usageServer(t, t.TempDir(), cfg)
	defer st.Close()
	if c, b, _ := do(t, "POST", hs.URL+"/admin/api/pricing/sync", ""); c != 200 || !strings.Contains(b, `"count":1`) {
		t.Fatalf("sync: %d %s", c, b)
	}
	_, pr := get(t, hs.URL+"/admin/api/pricing?model=brand-new-model-x")
	if !strings.Contains(pr, `"found":true`) || !strings.Contains(pr, `"input":4`) {
		t.Fatalf("lookup: %s", pr)
	}
}

func contextWithCancel() (ctx context.Context, cancel func()) {
	return context.WithCancel(context.Background())
}

type fakeTS struct{}

func (fakeTS) Token() (string, error)  { return "tok", nil }
func (fakeTS) Refresh() error          { return nil }
func (fakeTS) Meta() map[string]string { return nil }

func TestRefreshQuotaEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "claude", Type: "claude-oauth", OAuth: []string{"me@example.com"}}}
	path := t.TempDir() + "/config.yaml"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	config.Save(path, cfg)
	s := New(path, cfg, nil, slogDiscard(), nil, func(string, string) router.TokenSource { return fakeTS{} })
	fail := false
	s.QuotaFetch = func(ctx context.Context, id string) ([]router.QuotaWindow, error) {
		if fail {
			return nil, errors.New("HTTP 401: token sk-ant-oat01-SECRETSECRETSECRETSECRET expired")
		}
		u := 61.0
		return []router.QuotaWindow{{Name: "5h", UsedPct: &u}}, nil
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	c, body, _ := do(t, "POST", hs.URL+"/admin/api/accounts/claude:me@example.com/quota", "")
	if c != 200 || !strings.Contains(body, `"name":"5h"`) || !strings.Contains(body, `"used_pct":61`) {
		t.Fatalf("refresh: %d %s", c, body)
	}
	fail = true
	_, body, _ = do(t, "POST", hs.URL+"/admin/api/accounts/claude:me@example.com/quota", "")
	if !strings.Contains(body, "quota_error") || strings.Contains(body, "SECRETSECRET") || !strings.Contains(body, `"used_pct":61`) {
		t.Fatalf("error not recorded/redacted or old windows dropped: %s", body)
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
