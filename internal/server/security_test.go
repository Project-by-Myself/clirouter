package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

func do(t *testing.T, method, url, body string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b), resp.Header
}

func setAdminPassword(s *Server, pw string) {
	cfg := *s.h.Runtime().Cfg
	cfg.AdminPassword = pw
	s.swap(&cfg)
}

func TestAdminRejectsDNSRebindingCSRFAndProxies(t *testing.T) {
	_, hs := newTestServer(t)
	u := hs.URL + "/admin/api/status"
	if c, _, _ := do(t, "GET", u, ""); c != 200 {
		t.Fatalf("local admin: %d", c)
	}
	cases := map[string][]string{
		"rebinding host":   {"Host", "evil.example:8317"},
		"cross origin":     {"Origin", "http://evil.example"},
		"null origin":      {"Origin", "null"},
		"cross-site fetch": {"Sec-Fetch-Site", "cross-site"},
		"reverse proxy":    {"X-Forwarded-For", "203.0.113.9"},
	}
	for name, h := range cases {
		if c, _, _ := do(t, "GET", u, "", h...); c != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", name, c)
		}
	}
	// CSRF on a state-changing endpoint, as a cross-site <form> would send it.
	if c, _, _ := do(t, "POST", hs.URL+"/admin/api/accounts/x/reset", "", "Origin", "http://evil.example", "Content-Type", "text/plain"); c != http.StatusForbidden {
		t.Errorf("csrf reset: %d", c)
	}
	// The dashboard itself (same origin) keeps working.
	if c, _, _ := do(t, "GET", u, "", "Origin", hs.URL, "Sec-Fetch-Site", "same-origin"); c != 200 {
		t.Errorf("same origin: %d", c)
	}
	if c, _, _ := do(t, "GET", u, "", "Host", "localhost"); c != 200 {
		t.Errorf("localhost host: %d", c)
	}
}

func TestAdminPasswordAndBruteForce(t *testing.T) {
	s, hs := newTestServer(t)
	setAdminPassword(s, "correct horse battery staple")
	u := hs.URL + "/admin/api/status"
	if c, _, _ := do(t, "GET", u, ""); c != 401 {
		t.Fatalf("no password: %d", c)
	}
	// With a password, remote/proxied access is allowed (it is authenticated).
	if c, _, _ := do(t, "GET", u, "", "Authorization", "Bearer correct horse battery staple", "X-Forwarded-For", "203.0.113.9", "Host", "router.example"); c != 200 {
		t.Fatalf("good password: %d", c)
	}
	// Cross-origin requests are refused even with the password.
	if c, _, _ := do(t, "GET", u, "", "Authorization", "Bearer correct horse battery staple", "Origin", "http://evil.example"); c != 403 {
		t.Fatalf("cross origin with password: %d", c)
	}
	// One failure above (no password) + 9 guesses = 10: the limit.
	for i := 0; i < 9; i++ {
		if c, _, _ := do(t, "GET", u, "", "Authorization", "Bearer guess"); c != 401 {
			t.Fatalf("guess %d: %d", i, c)
		}
	}
	if c, _, _ := do(t, "GET", u, "", "Authorization", "Bearer correct horse battery staple"); c != 429 {
		t.Fatalf("after 10 failures: %d, want 429", c)
	}
}

func TestClientKeyBruteForceAndHash(t *testing.T) {
	s, hs := newTestServer(t)
	cfg := *s.h.Runtime().Cfg
	cfg.APIKeys = []config.ClientKey{{Name: "hashed", Key: config.HashClientKey("sk-clr-hashed-key")}}
	s.swap(&cfg)
	u := hs.URL + "/v1/models"
	if c, _, _ := do(t, "GET", u, "", "x-api-key", "sk-clr-hashed-key"); c != 200 {
		t.Fatalf("hashed key: %d", c)
	}
	if c, _, _ := do(t, "GET", u, "", "Authorization", "Bearer "+config.HashClientKey("sk-clr-hashed-key")); c != 401 {
		t.Fatalf("digest itself must not authenticate: %d", c)
	}
	for i := 0; i < 29; i++ {
		do(t, "GET", u, "", "x-api-key", "wrong")
	}
	if c, _, _ := do(t, "GET", u, "", "x-api-key", "sk-clr-hashed-key"); c != 429 {
		t.Fatalf("after 30 failures: %d, want 429", c)
	}
}

func TestNoKeysOnlyLocal(t *testing.T) {
	s, hs := newTestServer(t)
	cfg := *s.h.Runtime().Cfg
	cfg.APIKeys = nil
	s.swap(&cfg)
	u := hs.URL + "/v1/models"
	if c, _, _ := do(t, "GET", u, ""); c != 200 {
		t.Fatalf("local: %d", c)
	}
	for _, h := range [][]string{{"Host", "evil.example"}, {"Origin", "http://evil.example"}, {"X-Forwarded-For", "203.0.113.9"}} {
		if c, _, _ := do(t, "GET", u, "", h...); c != 403 {
			t.Errorf("%v: %d, want 403", h, c)
		}
	}
	// Simple cross-site POST (no preflight) must not reach the upstream.
	if c, _, _ := do(t, "POST", hs.URL+"/v1/chat/completions", `{"model":"claude-x"}`, "Origin", "http://evil.example", "Content-Type", "text/plain"); c != 403 {
		t.Errorf("csrf inference: %d", c)
	}
}

func TestMetricsRequireKey(t *testing.T) {
	_, hs := newTestServer(t)
	if c, _, _ := do(t, "GET", hs.URL+"/metrics", ""); c != 401 {
		t.Fatalf("metrics without key: %d", c)
	}
	if c, _, _ := do(t, "GET", hs.URL+"/metrics", "", "Authorization", "Bearer sk-test"); c != 200 {
		t.Fatalf("metrics with key: %d", c)
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, hs := newTestServer(t)
	_, _, h := do(t, "GET", hs.URL+"/", "")
	if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("headers: %v", h)
	}
	if _, _, h := do(t, "GET", hs.URL+"/admin/api/status", ""); h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("admin API must not send CORS headers")
	}
}

func TestMaskShowsOnlyLastChars(t *testing.T) {
	_, hs := newTestServer(t)
	_, body, _ := do(t, "GET", hs.URL+"/admin/api/config", "")
	if strings.Contains(body, "sk-a") || !strings.Contains(body, "…3456") {
		t.Fatalf("mask: %s", body)
	}
}

func TestExposedKeepsAdminPassword(t *testing.T) {
	s, hs := newTestServer(t)
	s.Exposed = true
	setAdminPassword(s, "pw-123456789")
	auth := []string{"Authorization", "Bearer pw-123456789"}
	_, body, _ := do(t, "GET", hs.URL+"/admin/api/config", "", auth...)
	body = strings.Replace(body, `"admin_password":"__keep__:0:****"`, `"admin_password":""`, 1)
	if c, b, _ := do(t, "PUT", hs.URL+"/admin/api/config", body, auth...); c != 400 {
		t.Fatalf("removing password while exposed: %d %s", c, b)
	}
}

func TestLoopbackHost(t *testing.T) {
	for h, want := range map[string]bool{
		"127.0.0.1:8317": true, "localhost": true, "LOCALHOST:1": true, "[::1]:8317": true, "app.localhost": true,
		"127.1.2.3": true, "evil.example": false, "127.0.0.1.evil.example": false, "0.0.0.0:8317": false, "": false, "10.0.0.1": false,
	} {
		if got := isLoopbackHost(h); got != want {
			t.Errorf("isLoopbackHost(%q) = %v", h, got)
		}
	}
	for a, want := range map[string]bool{"127.0.0.1:8317": true, "localhost:1": true, ":8317": false, "0.0.0.0:8317": false, "[::]:8317": false, "192.168.1.5:8317": false} {
		if got := IsLoopbackListen(a); got != want {
			t.Errorf("IsLoopbackListen(%q) = %v", a, got)
		}
	}
}

func TestSetTopLevelKeepsComments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("# keep me\nlisten: 0.0.0.0:8317 # inline\nproviders: []\n"), 0o644)
	if err := config.SetTopLevel(p, "admin_password", "s3cret"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "# keep me") || !strings.Contains(string(b), "admin_password: s3cret") {
		t.Fatalf("got:\n%s", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	cfg, err := config.Load(p)
	if err != nil || cfg.AdminPassword != "s3cret" {
		t.Fatalf("load: %v %q", err, cfg.AdminPassword)
	}
}
