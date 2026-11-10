package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.APIKeys = []config.ClientKey{{Name: "me", Key: "sk-test"}}
	cfg.Providers = []config.Provider{{ID: "ant", Type: "anthropic", Keys: []string{"sk-ant-secret-123456"}, Models: []string{"claude-x"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "index") })
	s := New(path, cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), static, nil)
	s.OAuthAdmin = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "oauth") })
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, hs
}

func get(t *testing.T, url string, hdr ...string) (int, string) {
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestRoutesAndAuth(t *testing.T) {
	_, hs := newTestServer(t)
	if c, b := get(t, hs.URL+"/"); c != 200 || b != "index" {
		t.Fatalf("static: %d %q", c, b)
	}
	if c, _ := get(t, hs.URL+"/v1/models"); c != 401 {
		t.Fatalf("models without key: %d", c)
	}
	if c, b := get(t, hs.URL+"/v1/models", "Authorization", "Bearer sk-test"); c != 200 || !strings.Contains(b, "claude-x") {
		t.Fatalf("models: %d %q", c, b)
	}
	if c, b := get(t, hs.URL+"/admin/api/oauth/accounts"); c != 200 || b != "oauth" {
		t.Fatalf("oauth admin: %d %q", c, b)
	}
	resp, _ := http.Post(hs.URL+"/nope", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 404 {
		t.Fatalf("POST unknown: %d", resp.StatusCode)
	}
}

func TestConfigSecretsMaskedAndPreserved(t *testing.T) {
	s, hs := newTestServer(t)
	_, body := get(t, hs.URL+"/admin/api/config")
	if strings.Contains(body, "sk-ant-secret-123456") || strings.Contains(body, "sk-test\"") {
		t.Fatalf("secret leaked: %s", body)
	}
	// Round-trip the masked config with one change; secrets must survive.
	body = strings.Replace(body, `"strategy":"round-robin"`, `"strategy":"fill-first"`, 1)
	req, _ := http.NewRequest("PUT", hs.URL+"/admin/api/config", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("put: %v %d %s", err, resp.StatusCode, b)
	}
	cfg := s.h.Runtime().Cfg
	if cfg.Routing.Strategy != "fill-first" || cfg.Providers[0].Keys[0] != "sk-ant-secret-123456" || cfg.APIKeys[0].Key != config.HashClientKey("sk-test") {
		t.Fatalf("config after put: %+v %+v", cfg.Routing, cfg.Providers[0].Keys)
	}
	saved, _ := os.ReadFile(s.path)
	if !strings.Contains(string(saved), "sk-ant-secret-123456") || !strings.Contains(string(saved), "fill-first") || strings.Contains(string(saved), "sk-test") {
		t.Fatalf("file not saved correctly:\n%s", saved)
	}
}
