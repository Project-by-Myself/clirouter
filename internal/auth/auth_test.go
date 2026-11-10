package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir(), nil)
	m.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return m
}

func setVar(t *testing.T, p *string, v string) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestStoreRoundTripAndTamper(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	c := &Credential{Type: TypeCodex, AccessToken: "at-secret", RefreshToken: "rt-secret",
		ExpiresAt: time.Unix(1_900_000_000, 0).UTC(), Email: "a@b.c", Extra: map[string]string{"k": "v"}}
	if err := s.Save("acct1", c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("acct1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != c.AccessToken || got.RefreshToken != c.RefreshToken || !got.ExpiresAt.Equal(c.ExpiresAt) || got.Extra["k"] != "v" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "auth", "acct1.json"))
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("a@b.c")) {
		t.Fatal("plaintext found in credential file")
	}
	for _, f := range []string{"acct1.json", ".key"} {
		st, err := os.Stat(filepath.Join(dir, "auth", f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s perm = %v", f, st.Mode().Perm())
		}
	}
	key, _ := os.ReadFile(filepath.Join(dir, "auth", ".key"))
	if len(key) != 32 {
		t.Fatalf("key len %d", len(key))
	}

	// A second store (fresh process) reuses the same key.
	if _, err := NewStore(dir).Load("acct1"); err != nil {
		t.Fatalf("reload with new store: %v", err)
	}

	// Tamper: flip a ciphertext byte.
	var env struct {
		V    int    `json:"v"`
		Data []byte `json:"data"`
	}
	json.Unmarshal(raw, &env)
	env.Data[len(env.Data)-5] ^= 0x01
	bad, _ := json.Marshal(env)
	os.WriteFile(filepath.Join(dir, "auth", "acct2.json"), raw, 0o600)
	os.WriteFile(filepath.Join(dir, "auth", "acct1.json"), bad, 0o600)
	if _, err := s.Load("acct1"); err == nil || !strings.Contains(err.Error(), "decryption failed") {
		t.Fatalf("tampered load err = %v", err)
	}
	// Swapped file: valid ciphertext bound to another id.
	if _, err := s.Load("acct2"); err == nil {
		t.Fatal("swapped file decrypted under a different id")
	}
	if _, err := s.Load("missing"); err != ErrNotFound {
		t.Fatalf("missing err = %v", err)
	}
	if err := s.Save("../evil", c); err == nil {
		t.Fatal("path traversal id accepted")
	}
	ids, _ := s.IDs()
	if strings.Join(ids, ",") != "acct1,acct2" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.json")
	if err := atomicWrite(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(p, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatalf("content %q", b)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", st.Mode().Perm())
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("leftover temp files: %v", ents)
	}
	// Rename onto a directory fails: original must be untouched and no temp left.
	if err := atomicWrite(filepath.Dir(p), []byte("x")); err == nil {
		t.Fatal("expected error writing over a directory")
	}
	ents, _ = os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover temp files after failure: %v", ents)
	}
}

func TestPKCE(t *testing.T) {
	// Expected value computed independently: printf %s v | openssl dgst -sha256 -binary | base64url.
	if got := pkceChallenge("dBjftJeZ4CVP-mJ92R2_bS6RKH4Ms4RnHmprEKz1zmZ"); got != "v05DaJ2DOaZNucH302rFPrLK2p91FeNWbEBBCqzUojQ" {
		t.Fatalf("challenge = %s", got)
	}
	v1, c1 := newPKCE()
	v2, _ := newPKCE()
	if len(v1) != 43 || v1 == v2 || c1 != pkceChallenge(v1) || strings.ContainsAny(v1+c1, "+/=") {
		t.Fatalf("bad pkce %q %q", v1, c1)
	}
}

func TestJWTClaims(t *testing.T) {
	tok := fakeJWT(map[string]any{
		"email": "user@example.com",
		"exp":   1_900_000_000,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-123",
			"chatgpt_plan_type":  "plus",
		},
	})
	email, acct, plan := codexIdentity(tok)
	if email != "user@example.com" || acct != "acc-123" || plan != "plus" {
		t.Fatalf("got %q %q %q", email, acct, plan)
	}
	if !jwtExpiry(tok).Equal(time.Unix(1_900_000_000, 0)) {
		t.Fatal("exp not parsed")
	}
	tok2 := fakeJWT(map[string]any{"https://api.openai.com/profile": map[string]any{"email": "p@x.y"}})
	if e, _, _ := codexIdentity(tok2); e != "p@x.y" {
		t.Fatalf("profile email %q", e)
	}
	if _, err := jwtClaims("not-a-jwt"); err == nil {
		t.Fatal("expected error")
	}
}

// codexTokenServer rotates refresh tokens and counts refreshes.
func codexTokenServer(t *testing.T, delay time.Duration, calls *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != UserAgent() {
			t.Errorf("User-Agent = %q", ua)
		}
		n := calls.Add(1)
		time.Sleep(delay)
		var grant string
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			grant = b["grant_type"]
			if b["client_id"] != CodexClientID || b["scope"] != "openid profile email" {
				t.Errorf("bad refresh body %v", b)
			}
		} else {
			r.ParseForm()
			grant = r.PostForm.Get("grant_type")
			if r.PostForm.Get("code_verifier") == "" || r.PostForm.Get("code") != "the-code" {
				t.Errorf("bad exchange form %v", r.PostForm)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-" + grant + "-" + string(rune('0'+n)),
			"refresh_token": "refresh-" + string(rune('0'+n)),
			"expires_in":    3600,
			"id_token": fakeJWT(map[string]any{"email": "dev@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-9", "chatgpt_plan_type": "pro"}}),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTokenNotLoggedIn(t *testing.T) {
	m := testManager(t)
	ts := m.TokenSource("p", "nobody")
	if _, err := ts.Token(); err == nil || err.Error() != "account nobody not logged in" {
		t.Fatalf("err = %v", err)
	}
	if ts.Meta()["status"] != "not logged in" {
		t.Fatal("meta status")
	}
}

func TestSingleFlightRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := codexTokenServer(t, 50*time.Millisecond, &calls)
	setVar(t, &CodexTokenURL, srv.URL)
	m := testManager(t)
	clk := &fakeClock{t: time.Now()}
	m.now = clk.Now
	if err := m.store.Save("c1", &Credential{Type: TypeCodex, AccessToken: "old", RefreshToken: "r0",
		ExpiresAt: clk.Now().Add(time.Minute), AccountID: "acct-9"}); err != nil {
		t.Fatal(err)
	}
	ts := m.TokenSource("codex", "c1")

	var wg sync.WaitGroup
	toks := make([]string, 50)
	for i := range toks {
		wg.Go(func() {
			tok, err := ts.Token()
			if err != nil {
				t.Error(err)
			}
			toks[i] = tok
		})
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	for _, tok := range toks {
		if tok != toks[0] || !strings.HasPrefix(tok, "access-refresh_token") {
			t.Fatalf("tokens differ: %q vs %q", tok, toks[0])
		}
	}
	// Rotated refresh token persisted.
	c, err := m.store.Load("c1")
	if err != nil || c.RefreshToken != "refresh-1" || c.Plan != "pro" || c.Email != "dev@example.com" {
		t.Fatalf("persisted %+v %v", c, err)
	}
	// Fresh token: no further refresh.
	ts.Token()
	if calls.Load() != 1 {
		t.Fatal("refreshed a fresh token")
	}
	// Forced refresh right after one coalesces.
	if err := ts.Refresh(); err != nil || calls.Load() != 1 {
		t.Fatalf("Refresh not coalesced: calls=%d err=%v", calls.Load(), err)
	}
	clk.Add(6 * time.Second)
	var wg2 sync.WaitGroup
	for range 20 {
		wg2.Go(func() {
			if err := ts.Refresh(); err != nil {
				t.Error(err)
			}
		})
	}
	wg2.Wait()
	if n := calls.Load(); n != 2 {
		t.Fatalf("concurrent forced refresh calls = %d, want 2", n)
	}
	md := ts.Meta()
	if md["header:Chatgpt-Account-Id"] != "acct-9" || md["email"] != "dev@example.com" || md["expires_at"] == "" {
		t.Fatalf("meta %v", md)
	}
}

func TestTokenFallsBackToValidTokenOnRefreshFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	}))
	defer srv.Close()
	setVar(t, &ClaudeTokenURL, srv.URL)
	m := testManager(t)
	m.store.Save("c", &Credential{Type: TypeClaude, AccessToken: "still-ok", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Minute)})
	ts := m.TokenSource("p", "c")
	if tok, err := ts.Token(); err != nil || tok != "still-ok" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if !strings.Contains(ts.Meta()["last_error"], "invalid_grant") {
		t.Fatalf("meta %v", ts.Meta())
	}
	m.store.Save("d", &Credential{Type: TypeClaude, AccessToken: "dead", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := m.TokenSource("p", "d").Token(); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v", err)
	}
}

func TestBackgroundRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := codexTokenServer(t, 0, &calls)
	setVar(t, &CodexTokenURL, srv.URL)
	m := testManager(t)
	m.tick = 5 * time.Millisecond
	m.bgJitter = 0
	m.store.Save("soon", &Credential{Type: TypeCodex, AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(10 * time.Minute)})
	m.store.Save("later", &Credential{Type: TypeCodex, AccessToken: "b", RefreshToken: "r", ExpiresAt: time.Now().Add(2 * time.Hour)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("background refresh calls = %d, want 1", n)
	}
	c, _ := m.store.Load("soon")
	if c.RefreshToken != "refresh-1" {
		t.Fatalf("soon not refreshed: %+v", c.RefreshToken)
	}
	if c, _ := m.store.Load("later"); c.AccessToken != "b" {
		t.Fatal("later refreshed early")
	}
}

func TestBackgroundBackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	setVar(t, &CodexTokenURL, srv.URL)
	m := testManager(t)
	m.tick = 2 * time.Millisecond
	m.backoffMin = 100 * time.Millisecond
	m.backoffMax = 200 * time.Millisecond
	m.store.Save("x", &Credential{Type: TypeCodex, AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(5 * time.Minute)})
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	time.Sleep(700 * time.Millisecond)
	cancel()
	n := calls.Load()
	// Without backoff ~350 attempts; with 100ms→200ms backoff about 4.
	if n < 2 || n > 8 {
		t.Fatalf("attempts = %d", n)
	}
	// Request path is not blocked by backoff: token still valid for 5m.
	if tok, err := m.TokenSource("p", "x").Token(); err != nil || tok != "a" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
}

func TestCodexCallbackFlow(t *testing.T) {
	var calls atomic.Int32
	srv := codexTokenServer(t, 0, &calls)
	setVar(t, &CodexTokenURL, srv.URL)
	setVar(t, &CodexCallbackAddr, "127.0.0.1:0")
	m := testManager(t)
	resp, err := m.StartFlow(StartRequest{Type: TypeCodex})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Mode != ModeRedirect {
		t.Fatalf("mode %s", resp.Mode)
	}
	u, _ := url.Parse(resp.AuthURL)
	q := u.Query()
	for k, v := range map[string]string{"client_id": CodexClientID, "redirect_uri": CodexRedirectURI,
		"code_challenge_method": "S256", "scope": CodexScope, "id_token_add_organizations": "true",
		"codex_cli_simplified_flow": "true", "response_type": "code"} {
		if q.Get(k) != v {
			t.Errorf("auth url %s = %q", k, q.Get(k))
		}
	}
	f := m.flow(resp.FlowID)
	h := m.callbackHandler(f)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/callback?code=the-code&state=wrong", nil))
	if rec.Code != 400 {
		t.Fatalf("bad state code %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/callback?code=the-code&state="+q.Get("state"), nil))
	if rec.Code != 200 {
		t.Fatalf("callback %d %s", rec.Code, rec.Body)
	}
	st, _ := m.FlowStatus(resp.FlowID)
	if st.State != StateDone || st.AccountID != "codex-dev-example.com" {
		t.Fatalf("status %+v", st)
	}
	md := m.TokenSource("p", st.AccountID).Meta()
	if md["header:Chatgpt-Account-Id"] != "acct-9" || md["plan"] != "pro" {
		t.Fatalf("meta %v", md)
	}
	select {
	case <-f.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("callback listener not shut down")
	}
}

func TestCodexPasteFallbackWhenPortBusy(t *testing.T) {
	var calls atomic.Int32
	srv := codexTokenServer(t, 0, &calls)
	setVar(t, &CodexTokenURL, srv.URL)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	setVar(t, &CodexCallbackAddr, busy.Addr().String())
	m := testManager(t)
	resp, err := m.StartFlow(StartRequest{Type: TypeCodex, AccountID: "mine"})
	if err != nil || resp.Mode != ModePasteCode {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	state := mustQuery(t, resp.AuthURL).Get("state")
	if _, err := m.CompleteFlow(context.Background(), resp.FlowID, "http://localhost:1455/auth/callback?code=the-code&state=nope"); err == nil {
		t.Fatal("state mismatch accepted")
	}
	info, err := m.CompleteFlow(context.Background(), resp.FlowID, "http://localhost:1455/auth/callback?code=the-code&state="+state)
	if err != nil || info.AccountID != "mine" || info.Email != "dev@example.com" {
		t.Fatalf("info %+v err %v", info, err)
	}
}

func mustQuery(t *testing.T, s string) url.Values {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestParseCodeInput(t *testing.T) {
	cases := map[string][2]string{
		"abc#xyz": {"abc", "xyz"},
		" abc ":   {"abc", ""},
		"http://localhost:1455/cb?code=c&state=s":                            {"c", "s"},
		"https://console.anthropic.com/oauth/code/callback?code=c2&state=s2": {"c2", "s2"},
		"code=c3&state=s3": {"c3", "s3"},
	}
	for in, want := range cases {
		c, s := parseCodeInput(in)
		if c != want[0] || s != want[1] {
			t.Errorf("%q -> %q %q", in, c, s)
		}
	}
}

func TestDeviceFlowSlowDown(t *testing.T) {
	var mu sync.Mutex
	var polls []time.Time
	mux := http.NewServeMux()
	mux.HandleFunc("/device/code", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("client_id") != GitHubClientID || r.Header.Get("Accept") != "application/json" {
			t.Errorf("device code req %v", r.PostForm)
		}
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-1234",
			"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 1})
	})
	mux.HandleFunc("/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.PostForm.Get("device_code") != "dc" {
			t.Errorf("poll form %v", r.PostForm)
		}
		mu.Lock()
		polls = append(polls, time.Now())
		n := len(polls)
		mu.Unlock()
		switch n {
		case 1:
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
		case 2:
			json.NewEncoder(w).Encode(map[string]any{"error": "slow_down"})
		default:
			json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_x", "token_type": "bearer"})
		}
	})
	mux.HandleFunc("/copilot", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "tid=abc", "expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints": map[string]any{"api": "https://api.individual.githubcopilot.com/"}})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"login": "octo"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	setVar(t, &GitHubDeviceCodeURL, srv.URL+"/device/code")
	setVar(t, &GitHubAccessTokenURL, srv.URL+"/access_token")
	setVar(t, &CopilotTokenURL, srv.URL+"/copilot")
	setVar(t, &GitHubUserURL, srv.URL+"/user")

	m := testManager(t)
	m.pollUnit = 10 * time.Millisecond
	resp, err := m.StartFlow(StartRequest{Type: TypeCopilot})
	if err != nil || resp.Mode != ModeDevice || resp.UserCode != "ABCD-1234" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	var st FlowStatus
	for range 200 {
		st, _ = m.FlowStatus(resp.FlowID)
		if st.State != StatePending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.State != StateDone || st.AccountID != "copilot-octo" {
		t.Fatalf("status %+v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(polls) != 3 {
		t.Fatalf("polls = %d", len(polls))
	}
	// interval 1 was raised below the 5-unit floor; slow_down adds 5 units -> 10 units (100ms).
	if gap := polls[2].Sub(polls[1]); gap < 95*time.Millisecond {
		t.Fatalf("slow_down not honored, gap %v", gap)
	}
	ts := m.TokenSource("p", "copilot-octo")
	tok, err := ts.Token()
	md := ts.Meta()
	if err != nil || tok != "tid=abc" || md["base_url"] != "https://api.individual.githubcopilot.com" ||
		md["header:Editor-Version"] != "clirouter/"+Version {
		t.Fatalf("tok=%q err=%v meta=%v", tok, err, md)
	}
	c, _ := m.store.Load("copilot-octo")
	if c.RefreshToken != "gho_x" {
		t.Fatal("github token not stored as refresh token")
	}
}

func TestAdminHandlerClaude(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["grant_type"] != "authorization_code" || b["code"] != "pasted" || b["code_verifier"] == "" ||
			b["client_id"] != ClaudeClientID || b["redirect_uri"] != ClaudeRedirectURI || b["state"] == "" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_request"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "sk-ant-oat", "refresh_token": "sk-ant-ort",
			"expires_in": 28800, "account": map[string]any{"email_address": "me@claude.test"},
			"organization": map[string]any{"uuid": "org-1"}})
	}))
	defer tokenSrv.Close()
	setVar(t, &ClaudeTokenURL, tokenSrv.URL)
	m := testManager(t)
	h := m.AdminHandler()
	call := func(method, path, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := call("POST", "/admin/api/oauth/start", `{"type":"claude"}`)
	if code != 200 || out["mode"] != ModePasteCode {
		t.Fatalf("start %d %v", code, out)
	}
	q := mustQuery(t, out["auth_url"].(string))
	if q.Get("code") != "true" || q.Get("client_id") != ClaudeClientID || q.Get("scope") != ClaudeScope {
		t.Fatalf("auth url %v", q)
	}
	flowID := out["flow_id"].(string)
	code, out = call("POST", "/admin/api/oauth/complete", `{"flow_id":"`+flowID+`","code":"pasted#`+q.Get("state")+`"}`)
	if code != 200 || out["account_id"] != "claude-me-claude.test" || out["email"] != "me@claude.test" {
		t.Fatalf("complete %d %v", code, out)
	}
	code, out = call("GET", "/admin/api/oauth/status?flow_id="+flowID, "")
	if code != 200 || out["state"] != StateDone {
		t.Fatalf("status %d %v", code, out)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/accounts", nil))
	var list []AccountInfo
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list) != 1 || list[0].Type != TypeClaude || list[0].ExpiresAt == nil {
		t.Fatalf("accounts %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-ant") {
		t.Fatal("token leaked in accounts listing")
	}
	if code, _ := call("DELETE", "/admin/api/oauth/accounts/claude-me-claude.test", ""); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if code, _ := call("DELETE", "/admin/api/oauth/accounts/claude-me-claude.test", ""); code != 404 {
		t.Fatalf("second delete %d", code)
	}
	if _, err := m.TokenSource("p", "claude-me-claude.test").Token(); err == nil {
		t.Fatal("deleted account still yields token")
	}
	if code, _ := call("GET", "/admin/api/oauth/status?flow_id=nope", ""); code != 404 {
		t.Fatalf("unknown flow %d", code)
	}
	if code, _ := call("POST", "/admin/api/oauth/start", `{"type":"bogus"}`); code != 400 {
		t.Fatalf("bogus type %d", code)
	}
}

func TestFlowExpiry(t *testing.T) {
	m := testManager(t)
	clk := &fakeClock{t: time.Now()}
	m.now = clk.Now
	resp, err := m.StartFlow(StartRequest{Type: TypeClaude})
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(16 * time.Minute)
	if _, ok := m.FlowStatus(resp.FlowID); ok {
		t.Fatal("flow did not expire")
	}
}

func TestGeminiLoginOnboarding(t *testing.T) {
	var onboardCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("client_secret") != GeminiClientSecret || r.PostForm.Get("code_verifier") == "" {
			t.Errorf("token form %v", r.PostForm)
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29", "refresh_token": "1//r", "expires_in": 3599})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"email": "g@gmail.test"})
	})
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"allowedTiers": []any{map[string]any{"id": "free-tier", "isDefault": true}}})
	})
	mux.HandleFunc("/v1internal:onboardUser", func(w http.ResponseWriter, r *http.Request) {
		if onboardCalls.Add(1) < 2 {
			json.NewEncoder(w).Encode(map[string]any{"name": "op", "done": false})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"done": true, "response": map[string]any{"cloudaicompanionProject": map[string]any{"id": "proj-42"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	setVar(t, &GoogleTokenURL, srv.URL+"/token")
	setVar(t, &GoogleUserInfoURL, srv.URL+"/userinfo")
	setVar(t, &CodeAssistBaseURL, srv.URL)
	m := testManager(t)
	m.pollUnit = time.Millisecond
	resp, err := m.StartFlow(StartRequest{Type: TypeGeminiCLI})
	if err != nil || resp.Mode != ModeRedirect {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	q := mustQuery(t, resp.AuthURL)
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || q.Get("access_type") != "offline" {
		t.Fatalf("auth url %v", q)
	}
	// Hit the real loopback listener like a browser would.
	r, err := http.Get(redirect + "?code=gc&state=" + q.Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("callback status %d", r.StatusCode)
	}
	st, _ := m.FlowStatus(resp.FlowID)
	if st.State != StateDone || st.Account.ProjectID != "proj-42" || st.AccountID != "gemini-cli-g-gmail.test" {
		t.Fatalf("status %+v %+v", st, st.Account)
	}
	if md := m.TokenSource("p", st.AccountID).Meta(); md["project"] != "proj-42" {
		t.Fatalf("meta %v", md)
	}
}
