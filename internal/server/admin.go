package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/store"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	a := func(h http.HandlerFunc) http.HandlerFunc { return s.adminAuth(h) }
	mux.HandleFunc("GET /admin/api/status", a(s.adminStatus))
	mux.HandleFunc("GET /admin/api/config", a(s.adminGetConfig))
	mux.HandleFunc("PUT /admin/api/config", a(s.adminPutConfig))
	mux.HandleFunc("GET /admin/api/accounts", a(s.adminAccounts))
	mux.HandleFunc("POST /admin/api/accounts/{id}/reset", a(s.adminResetAccount))
	mux.HandleFunc("POST /admin/api/accounts/{id}/quota", a(s.adminRefreshQuota))
	mux.HandleFunc("GET /admin/api/requests", a(s.adminRequests))
	mux.HandleFunc("GET /admin/api/live", a(s.adminLive))
	mux.HandleFunc("GET /admin/api/stats", a(s.adminStats))
	mux.HandleFunc("GET /admin/api/models", a(s.adminModels))
	mux.HandleFunc("GET /admin/api/keys/usage", a(s.adminKeysUsage))
	mux.HandleFunc("GET /admin/api/providers/usage", a(s.adminProvidersUsage))
	mux.HandleFunc("GET /admin/api/usage", a(s.adminUsage))
	mux.HandleFunc("GET /admin/api/pricing", a(s.adminPricing))
	mux.HandleFunc("POST /admin/api/pricing/sync", a(s.adminPricingSync))
	mux.HandleFunc("POST /admin/api/alerts/test", a(s.adminTestAlert))
	mux.HandleFunc("GET /admin/api/keys", a(s.adminListKeys))
	mux.HandleFunc("POST /admin/api/keys", a(s.adminCreateKey))
	mux.HandleFunc("GET /admin/api/keys/{name}", a(s.adminGetKey))
	mux.HandleFunc("PATCH /admin/api/keys/{name}", a(s.adminUpdateKey))
	mux.HandleFunc("DELETE /admin/api/keys/{name}", a(s.adminDeleteKey))
	mux.HandleFunc("POST /admin/api/keys/{name}/rotate", a(s.adminRotateKey))
	if s.OAuthAdmin != nil {
		mux.Handle("/admin/api/oauth/", a(s.OAuthAdmin.ServeHTTP))
	}
}

// adminAuth guards the admin API (which can read logs, rewrite provider
// base URLs and so exfiltrate keys):
//   - browser requests from another origin are always refused (CSRF);
//   - with admin_password set, "Authorization: Bearer <pw>" is required,
//     compared in constant time and throttled per IP after failures;
//   - without it, only direct local requests are allowed: loopback peer,
//     loopback Host header (DNS rebinding) and no reverse-proxy headers.
func (s *Server) adminAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if crossOrigin(r) {
			jsonErr(w, http.StatusForbidden, "cross-origin admin requests are not allowed")
			return
		}
		pw := config.Secret(s.h.Runtime().Cfg.AdminPassword)
		if pw == "" {
			if !localRequest(r) {
				jsonErr(w, http.StatusForbidden, "admin API is only reachable directly from localhost (http://127.0.0.1) unless admin_password is set")
				return
			}
		} else {
			ip := clientIP(r)
			if s.adminFails.blocked(ip) {
				w.Header().Set("Retry-After", "300")
				jsonErr(w, http.StatusTooManyRequests, "too many failed login attempts; try again later")
				return
			}
			a := r.Header.Get("Authorization")
			got := ""
			if len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
				got = strings.TrimSpace(a[7:])
			}
			if !config.SecretEqual(got, pw) {
				s.adminFails.fail(ip)
				jsonErr(w, http.StatusUnauthorized, "invalid admin password")
				return
			}
		}
		h(w, r)
	}
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	rt := s.h.Runtime()
	accounts, cooling := 0, 0
	now := time.Now()
	for _, p := range rt.Router.Providers() {
		for _, a := range p.Accounts {
			accounts++
			if st := a.Status(); st.CooldownUntil != nil && st.CooldownUntil.After(now) {
				cooling++
			}
		}
	}
	var dropped int64
	if s.store != nil {
		dropped = s.store.Dropped()
	}
	listen := rt.Cfg.Listen
	if s.ListenAddr != "" {
		listen = s.ListenAddr
	}
	jsonOK(w, map[string]any{
		"version":          Version,
		"uptime_s":         int64(time.Since(s.started).Seconds()),
		"listen":           listen,
		"config_path":      s.path,
		"metrics":          s.h.Metrics.Snapshot(),
		"dropped_records":  dropped,
		"providers":        len(rt.Router.Providers()),
		"accounts":         accounts,
		"accounts_cooling": cooling,
		"auth_required":    len(rt.Cfg.APIKeys) > 0,
	})
}

const keepSecret = "__keep__"

// masked returns a copy of cfg with secrets replaced by
// "__keep__:<index>[@<hex owner id>]:<hint>". Provider keys carry the id of
// the provider they belong to, so renaming a provider keeps its keys.
func masked(cfg *config.Config) *config.Config {
	b, _ := json.Marshal(cfg)
	var c config.Config
	json.Unmarshal(b, &c)
	hide := func(i int, owner, v string) string {
		// References (env vars, service-account file paths) are not secrets.
		if strings.HasPrefix(v, "env:") || (owner != "" && (strings.HasPrefix(v, config.ServiceAccountPrefix) || v == config.AWSKeyPrefix+"env")) {
			return v
		}
		hint := "****"
		if len(v) >= 16 { // only the last 4 characters, and only of long secrets
			hint = "…" + v[len(v)-4:]
		}
		ref := strconv.Itoa(i)
		if owner != "" {
			ref += "@" + hex.EncodeToString([]byte(owner))
		}
		return keepSecret + ":" + ref + ":" + hint
	}
	for pi := range c.Providers {
		for i, k := range c.Providers[pi].Keys {
			c.Providers[pi].Keys[i] = hide(i, c.Providers[pi].ID, k)
		}
	}
	for i, k := range c.APIKeys {
		c.APIKeys[i].Key = hide(i, "", k.Key)
	}
	if c.AdminPassword != "" && !strings.HasPrefix(c.AdminPassword, "env:") {
		c.AdminPassword = keepSecret + ":0:****" // no hint: may be a short, human-chosen password
	}
	// Webhook URLs (Slack, Discord…) and bot tokens are credentials too.
	if v := c.Alerts.WebhookURL; v != "" {
		c.Alerts.WebhookURL = hide(0, "", v)
	}
	if v := c.Alerts.TelegramBotToken; v != "" {
		c.Alerts.TelegramBotToken = hide(0, "", v)
	}
	return &c
}

// unmask restores "__keep__:<index>:..." placeholders from the current config.
func unmask(next, cur *config.Config) {
	// parse splits a placeholder's "<index>[@<hex owner id>]" reference.
	parse := func(v string) (idx int, owner string, hasOwner, ok bool) {
		rest, ok := strings.CutPrefix(v, keepSecret+":")
		if !ok {
			return 0, "", false, false
		}
		ref, _, _ := strings.Cut(rest, ":")
		ref, ownerHex, hasOwner := strings.Cut(ref, "@")
		if hasOwner {
			b, err := hex.DecodeString(ownerHex)
			if err != nil {
				return -1, "", false, true
			}
			owner = string(b)
		}
		i, err := strconv.Atoi(ref)
		if err != nil {
			i = -1
		}
		return i, owner, hasOwner, true
	}
	restore := func(v string, pool []string) string {
		i, _, _, ok := parse(v)
		if !ok {
			return v
		}
		if i >= 0 && i < len(pool) {
			return pool[i]
		}
		return ""
	}
	curProv := map[string]config.Provider{}
	for _, p := range cur.Providers {
		curProv[p.ID] = p
	}
	for pi := range next.Providers {
		keys := next.Providers[pi].Keys[:0]
		for _, k := range next.Providers[pi].Keys {
			// The placeholder names the provider the key came from, so a
			// renamed provider still finds its keys.
			owner := next.Providers[pi].ID
			if _, o, has, ok := parse(k); ok && has {
				owner = o
			}
			if v := restore(k, curProv[owner].Keys); v != "" {
				keys = append(keys, v)
			}
		}
		next.Providers[pi].Keys = keys
	}
	curKeys := make([]string, len(cur.APIKeys))
	for i, k := range cur.APIKeys {
		curKeys[i] = k.Key
	}
	for i := range next.APIKeys {
		next.APIKeys[i].Key = restore(next.APIKeys[i].Key, curKeys)
	}
	next.AdminPassword = restore(next.AdminPassword, []string{cur.AdminPassword})
	next.Alerts.WebhookURL = restore(next.Alerts.WebhookURL, []string{cur.Alerts.WebhookURL})
	next.Alerts.TelegramBotToken = restore(next.Alerts.TelegramBotToken, []string{cur.Alerts.TelegramBotToken})
}

func (s *Server) adminGetConfig(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, masked(s.h.Runtime().Cfg))
}

func (s *Server) adminPutConfig(w http.ResponseWriter, r *http.Request) {
	next := config.Default()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := dec.Decode(next); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.h.Runtime().Cfg
	unmask(next, cur)
	if next.DataDir == "" {
		next.DataDir = cur.DataDir
	}
	// New client keys are stored hashed; the dashboard shows a key only once.
	for i, k := range next.APIKeys {
		if k.Key != "" && !strings.HasPrefix(k.Key, "env:") && !strings.HasPrefix(k.Key, config.HashPrefix) {
			next.APIKeys[i].Key = config.HashClientKey(k.Key)
		}
		if k.CreatedAt == "" {
			next.APIKeys[i].CreatedAt = time.Now().Format(time.RFC3339)
		}
	}
	if status, err := s.commit(next); err != nil {
		jsonErr(w, status, err.Error())
		return
	}
	jsonOK(w, masked(next))
}

// commit validates next, writes it to disk and makes it live. Caller holds s.mu.
func (s *Server) commit(next *config.Config) (int, error) {
	if err := next.Validate(); err != nil {
		return http.StatusBadRequest, err
	}
	if s.Exposed && config.Secret(next.AdminPassword) == "" {
		return http.StatusBadRequest, errors.New("admin_password cannot be removed while clirouter listens on a non-loopback address")
	}
	// data_dir defaults to <config dir>/data at load time; do not pin that
	// derived absolute path into the file.
	toSave := *next
	if toSave.DataDir == filepath.Join(filepath.Dir(s.path), "data") {
		toSave.DataDir = ""
	}
	if err := config.Save(s.path, &toSave); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("save failed: %w", err)
	}
	s.swap(next)
	if fi, err := statPath(s.path); err == nil {
		s.modTime = fi
	}
	return 0, nil
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	var out []any
	for _, p := range s.h.Runtime().Router.Providers() {
		for _, a := range p.Accounts {
			out = append(out, a.Status())
		}
	}
	if out == nil {
		out = []any{}
	}
	jsonOK(w, out)
}

func (s *Server) adminResetAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, p := range s.h.Runtime().Router.Providers() {
		for _, a := range p.Accounts {
			if a.ID == id {
				a.Reset()
				jsonOK(w, a.Status())
				return
			}
		}
	}
	jsonErr(w, http.StatusNotFound, "account not found")
}

func (s *Server) adminRequests(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		jsonOK(w, []store.Record{})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	recs, err := s.store.Requests(r.Context(), store.Query{Limit: limit, BeforeID: before, Provider: q.Get("provider"), Model: q.Get("model"), Errors: q.Get("errors") == "1"})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if recs == nil {
		recs = []store.Record{}
	}
	jsonOK(w, recs)
}

func (s *Server) adminLive(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 1000 {
		n = 100
	}
	if s.store == nil {
		jsonOK(w, []store.Record{})
		return
	}
	jsonOK(w, s.store.Live(n))
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		jsonErr(w, http.StatusServiceUnavailable, "store disabled")
		return
	}
	ranges := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	d, ok := ranges[r.URL.Query().Get("range")]
	if !ok {
		d = 24 * time.Hour
	}
	st, err := s.store.Stats(r.Context(), time.Now().Add(-d))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, st)
}

func (s *Server) adminModels(w http.ResponseWriter, r *http.Request) {
	type m struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
	}
	var out []m
	for _, n := range s.modelNames() {
		out = append(out, m{n.id, n.owner})
	}
	if out == nil {
		out = []m{}
	}
	jsonOK(w, out)
}
