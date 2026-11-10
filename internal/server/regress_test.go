package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

// Renaming a provider in the dashboard sends its keys back as masked
// placeholders under the new id; they must still resolve to the old keys.
func TestRenameProviderKeepsKeys(t *testing.T) {
	s, hs := newTestServer(t)
	_, body := get(t, hs.URL+"/admin/api/config")
	var cfg config.Config
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Providers[0].ID = "ant-renamed"
	b, _ := json.Marshal(cfg)
	if c, out, _ := do(t, "PUT", hs.URL+"/admin/api/config", string(b)); c != 200 {
		t.Fatalf("put: %d %s", c, out)
	}
	got := s.h.Runtime().Cfg.Providers[0]
	if got.ID != "ant-renamed" || len(got.Keys) != 1 || got.Keys[0] != "sk-ant-secret-123456" {
		t.Fatalf("keys lost on rename: %+v", got)
	}
	saved, _ := os.ReadFile(s.path)
	if !strings.Contains(string(saved), "sk-ant-secret-123456") {
		t.Fatalf("key not saved:\n%s", saved)
	}
}

// Placeholders without an owner (older dashboards) still resolve by the
// provider's own id.
func TestUnmaskLegacyPlaceholder(t *testing.T) {
	cur := &config.Config{Providers: []config.Provider{{ID: "a", Keys: []string{"k0", "k1"}}}}
	next := &config.Config{Providers: []config.Provider{{ID: "a", Keys: []string{"__keep__:1:****", "new"}}}}
	unmask(next, cur)
	if k := next.Providers[0].Keys; len(k) != 2 || k[0] != "k1" || k[1] != "new" {
		t.Fatalf("got %v", k)
	}
}

// The derived default data_dir must not be pinned into config.yaml.
func TestPutConfigDoesNotPersistDefaultDataDir(t *testing.T) {
	s, hs := newTestServer(t)
	cfg := *s.h.Runtime().Cfg
	cfg.DataDir = ""
	if err := config.Save(s.path, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	_, body := get(t, hs.URL+"/admin/api/config")
	if c, out, _ := do(t, "PUT", hs.URL+"/admin/api/config", body); c != 200 {
		t.Fatalf("put: %d %s", c, out)
	}
	saved, _ := os.ReadFile(s.path)
	if strings.Contains(string(saved), "data_dir") {
		t.Fatalf("data_dir persisted:\n%s", saved)
	}
}

// Behind a reverse proxy every request comes from loopback; one client
// sending bad keys must not lock out the others.
func TestAuthThrottleBehindReverseProxyIsPerClient(t *testing.T) {
	s, hs := newTestServer(t)
	setAdminPassword(s, "pw-123")
	for i := 0; i < 40; i++ {
		do(t, "GET", hs.URL+"/v1/models", "", "Authorization", "Bearer wrong", "X-Forwarded-For", "203.0.113.7")
		do(t, "GET", hs.URL+"/admin/api/status", "", "Authorization", "Bearer wrong", "X-Forwarded-For", "203.0.113.7")
	}
	if c, _, _ := do(t, "GET", hs.URL+"/v1/models", "", "Authorization", "Bearer wrong", "X-Forwarded-For", "203.0.113.7"); c != http.StatusTooManyRequests {
		t.Fatalf("attacker not throttled: %d", c)
	}
	if c, _, _ := do(t, "GET", hs.URL+"/v1/models", "", "Authorization", "Bearer sk-test", "X-Forwarded-For", "198.51.100.2"); c != 200 {
		t.Fatalf("other client locked out of inference: %d", c)
	}
	if c, _, _ := do(t, "GET", hs.URL+"/admin/api/status", "", "Authorization", "Bearer pw-123", "X-Forwarded-For", "198.51.100.2"); c != 200 {
		t.Fatalf("other client locked out of admin: %d", c)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct{ remote, xff, real, want string }{
		{"127.0.0.1:5", "", "", "127.0.0.1"},
		{"127.0.0.1:5", "1.1.1.1, 203.0.113.7", "", "203.0.113.7"}, // proxy appended the last hop
		{"[::1]:5", "", "203.0.113.8", "203.0.113.8"},
		{"198.51.100.9:5", "1.2.3.4", "", "198.51.100.9"}, // not from a local proxy: header ignored
		{"127.0.0.1:5", "garbage", "", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.real != "" {
			r.Header.Set("X-Real-Ip", c.real)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

// /admin/api/status reports the address actually listened on (--listen),
// while the config keeps the file's value.
func TestStatusReportsEffectiveListen(t *testing.T) {
	s, hs := newTestServer(t)
	s.ListenAddr = "0.0.0.0:9999"
	_, body := get(t, hs.URL+"/admin/api/status")
	if !strings.Contains(body, `"listen":"0.0.0.0:9999"`) {
		t.Fatalf("status: %s", body)
	}
	_, body = get(t, hs.URL+"/admin/api/config")
	if !strings.Contains(body, `"listen":"127.0.0.1:8317"`) {
		t.Fatalf("config: %s", body)
	}
}
