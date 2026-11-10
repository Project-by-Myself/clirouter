package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
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
	mux.HandleFunc("GET /admin/api/requests", a(s.adminRequests))
	mux.HandleFunc("GET /admin/api/live", a(s.adminLive))
	mux.HandleFunc("GET /admin/api/stats", a(s.adminStats))
	mux.HandleFunc("GET /admin/api/models", a(s.adminModels))
	if s.OAuthAdmin != nil {
		mux.Handle("/admin/api/oauth/", a(s.OAuthAdmin.ServeHTTP))
	}
}

// adminAuth: with admin_password set, require "Authorization: Bearer <pw>";
// without it, only loopback clients may use the admin API.
func (s *Server) adminAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pw := config.Secret(s.h.Runtime().Cfg.AdminPassword)
		if pw == "" {
			if !isLoopback(r) {
				jsonErr(w, http.StatusForbidden, "admin API is only reachable from localhost unless admin_password is set")
				return
			}
		} else {
			got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(pw)) != 1 {
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
	jsonOK(w, map[string]any{
		"version":          Version,
		"uptime_s":         int64(time.Since(s.started).Seconds()),
		"listen":           rt.Cfg.Listen,
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

// masked returns a copy of cfg with secrets replaced by "__keep__:<index>:<hint>".
func masked(cfg *config.Config) *config.Config {
	b, _ := json.Marshal(cfg)
	var c config.Config
	json.Unmarshal(b, &c)
	hide := func(i int, v string) string {
		if strings.HasPrefix(v, "env:") {
			return v
		}
		hint := "****"
		if len(v) > 8 {
			hint = v[:4] + "…" + v[len(v)-4:]
		}
		return keepSecret + ":" + strconv.Itoa(i) + ":" + hint
	}
	for pi := range c.Providers {
		for i, k := range c.Providers[pi].Keys {
			c.Providers[pi].Keys[i] = hide(i, k)
		}
	}
	for i, k := range c.APIKeys {
		c.APIKeys[i].Key = hide(i, k.Key)
	}
	if c.AdminPassword != "" {
		c.AdminPassword = hide(0, c.AdminPassword)
	}
	return &c
}

// unmask restores "__keep__:<index>:..." placeholders from the current config.
func unmask(next, cur *config.Config) {
	restore := func(v string, pool []string) string {
		rest, ok := strings.CutPrefix(v, keepSecret+":")
		if !ok {
			return v
		}
		idx, _, _ := strings.Cut(rest, ":")
		if i, err := strconv.Atoi(idx); err == nil && i >= 0 && i < len(pool) {
			return pool[i]
		}
		return ""
	}
	curProv := map[string]config.Provider{}
	for _, p := range cur.Providers {
		curProv[p.ID] = p
	}
	for pi := range next.Providers {
		pool := curProv[next.Providers[pi].ID].Keys
		keys := next.Providers[pi].Keys[:0]
		for _, k := range next.Providers[pi].Keys {
			if v := restore(k, pool); v != "" {
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
	if err := next.Validate(); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.Save(s.path, next); err != nil {
		jsonErr(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	s.swap(next)
	if fi, err := statPath(s.path); err == nil {
		s.modTime = fi
	}
	jsonOK(w, masked(next))
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
