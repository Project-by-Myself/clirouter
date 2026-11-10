// Package server wires HTTP routes: the inference API (data plane), the
// admin API and the embedded dashboard (control plane). The admin side never
// sits in front of inference requests.
package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/proxy"
	"github.com/Project-by-Myself/clirouter/internal/router"
	"github.com/Project-by-Myself/clirouter/internal/store"
	"github.com/Project-by-Myself/clirouter/internal/upstream"
)

const Version = "0.1.0"

type Server struct {
	path    string
	h       *proxy.Handler
	store   *store.Store
	started time.Time
	log     *slog.Logger
	mu      sync.Mutex // serializes reloads
	modTime time.Time
	oauth   func(providerID, accountID string) router.TokenSource
	static  http.Handler
	// OAuthAdmin, when set, serves /admin/api/oauth/ (behind admin auth).
	OAuthAdmin http.Handler
	// Exposed is true when the server listens on a non-loopback address; the
	// admin password can then not be removed from the dashboard.
	Exposed bool
	// ListenAddr is the address actually listened on (it differs from the
	// config's listen when overridden by --listen, which is not persisted).
	ListenAddr string

	adminFails  *failLimiter
	clientFails *failLimiter
}

func New(path string, cfg *config.Config, st *store.Store, log *slog.Logger, static http.Handler, oauth func(string, string) router.TokenSource) *Server {
	s := &Server{path: path, store: st, started: time.Now(), log: log, static: static, oauth: oauth,
		adminFails: newFailLimiter(10, 5*time.Minute), clientFails: newFailLimiter(30, time.Minute)}
	rt := &proxy.Runtime{Cfg: cfg, Router: router.New(cfg, nil, oauth), Clients: upstream.NewClients(cfg.Timeouts.Connect.D())}
	s.h = proxy.NewHandler(rt, st)
	if fi, err := os.Stat(path); err == nil {
		s.modTime = fi.ModTime()
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.inference(ir.FormatChat))
	mux.HandleFunc("POST /chat/completions", s.inference(ir.FormatChat))
	mux.HandleFunc("POST /v1/messages", s.inference(ir.FormatAnthropic))
	mux.HandleFunc("POST /v1/messages/count_tokens", s.countTokens)
	mux.HandleFunc("POST /v1/responses", s.inference(ir.FormatResponses))
	mux.HandleFunc("POST /responses", s.inference(ir.FormatResponses))
	mux.HandleFunc("POST /v1beta/models/{action}", s.gemini)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /v1beta/models", s.models)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.clientAuth(w, r, ir.FormatChat); !ok {
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		s.h.Metrics.WritePrometheus(w)
	})
	s.adminRoutes(mux)
	if s.static != nil {
		// Method-less "/" so it never conflicts with method-less admin
		// subtrees; non-GET requests to unknown paths get a 404.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.NotFound(w, r)
				return
			}
			s.static.ServeHTTP(w, r)
		})
	}
	return securityHeaders(mux)
}

// clientAuth identifies the client key. With no api_keys configured only
// local, non-browser-cross-origin requests are served (so a web page cannot
// spend the user's credits via CSRF or DNS rebinding). On failure it writes
// the error response in format f and returns ok=false.
func (s *Server) clientAuth(w http.ResponseWriter, r *http.Request, f ir.Format) (string, bool) {
	keys := s.h.Runtime().Cfg.APIKeys
	if len(keys) == 0 {
		if !localRequest(r) || crossOrigin(r) {
			writeErr(w, f, http.StatusForbidden, "permission_error", "no api_keys are configured, so clirouter only accepts requests from localhost; add a client key to allow other clients")
			return "", false
		}
		return "local", true
	}
	ip := clientIP(r)
	if s.clientFails.blocked(ip) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, f, http.StatusTooManyRequests, "rate_limit_error", "too many failed authentication attempts; try again later")
		return "", false
	}
	cand := []string{r.Header.Get("X-Api-Key"), r.Header.Get("X-Goog-Api-Key"), r.URL.Query().Get("key")}
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		cand = append(cand, strings.TrimSpace(a[7:]))
	}
	name := ""
	for _, c := range cand {
		if c == "" {
			continue
		}
		// No early exit: every key is compared so timing does not reveal
		// which entry matched.
		for _, k := range keys {
			if config.ClientKeyMatches(c, k.Key) && name == "" {
				name = k.Name
			}
		}
	}
	if name == "" {
		s.clientFails.fail(ip)
		writeErr(w, f, http.StatusUnauthorized, "authentication_error", "invalid or missing API key")
		return "", false
	}
	return name, true
}

func (s *Server) inference(f ir.Format) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, ok := s.clientAuth(w, r, f)
		if !ok {
			return
		}
		s.h.Serve(w, r, f, "", false, name)
	}
}

func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	name, ok := s.clientAuth(w, r, ir.FormatGemini)
	if !ok {
		return
	}
	model, method, found := strings.Cut(r.PathValue("action"), ":")
	if !found || (method != "generateContent" && method != "streamGenerateContent") {
		writeErr(w, ir.FormatGemini, http.StatusNotFound, "not_found_error", "unsupported method "+method)
		return
	}
	s.h.Serve(w, r, ir.FormatGemini, model, method == "streamGenerateContent", name)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.clientAuth(w, r, ir.FormatChat); !ok {
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
		// Anthropic SDK shape
		Type        string `json:"type"`
		DisplayName string `json:"display_name"`
	}
	names := s.modelNames()
	out := make([]model, 0, len(names))
	for _, n := range names {
		out = append(out, model{ID: n.id, Object: "model", Created: s.started.Unix(), OwnedBy: n.owner, Type: "model", DisplayName: n.id})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": out, "has_more": false})
}

type modelName struct{ id, owner string }

func (s *Server) modelNames() []modelName {
	cfg := s.h.Runtime().Cfg
	seen := map[string]bool{}
	var out []modelName
	add := func(id, owner string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, modelName{id, owner})
		}
	}
	for _, c := range cfg.Combos {
		add(c.Name, "combo")
	}
	aliases := make([]string, 0, len(cfg.Aliases))
	for a := range cfg.Aliases {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, a := range aliases {
		add(a, "alias")
	}
	for _, p := range cfg.Providers {
		if p.Disabled {
			continue
		}
		for _, m := range p.Models {
			add(m, p.ID)
			add(p.ID+"/"+m, p.ID)
		}
		for m := range p.ModelMap {
			add(m, p.ID)
		}
	}
	return out
}

// countTokens forwards to Anthropic when the model routes there, otherwise
// returns a cheap estimate (Claude Code only uses it for context display).
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.clientAuth(w, r, ir.FormatAnthropic); !ok {
		return
	}
	s.h.CountTokens(w, r)
}

func writeErr(w http.ResponseWriter, f ir.Format, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(proxy.ErrorBody(f, status, typ, msg))
}

// Reload re-reads the config file and swaps the runtime atomically. In-flight
// requests keep the runtime they started with.
func (s *Server) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := config.Load(s.path)
	if err != nil {
		return err
	}
	s.swap(cfg)
	if fi, err := os.Stat(s.path); err == nil {
		s.modTime = fi.ModTime()
	}
	return nil
}

func (s *Server) swap(cfg *config.Config) {
	old := s.h.Runtime()
	clients := old.Clients
	if cfg.Timeouts.Connect != old.Cfg.Timeouts.Connect {
		clients = upstream.NewClients(cfg.Timeouts.Connect.D())
	}
	s.h.SetRuntime(&proxy.Runtime{Cfg: cfg, Router: router.New(cfg, old.Router, s.oauth), Clients: clients})
}

// Watch polls the config file and reloads on change (no fsnotify dependency;
// a 2s poll is negligible and robust to editors that replace files).
func (s *Server) Watch(stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			fi, err := os.Stat(s.path)
			if err != nil {
				continue
			}
			s.mu.Lock()
			changed := !fi.ModTime().Equal(s.modTime)
			s.mu.Unlock()
			if changed {
				if err := s.Reload(); err != nil {
					s.log.Error("config reload failed", "err", err)
					s.mu.Lock()
					s.modTime = fi.ModTime()
					s.mu.Unlock()
				} else {
					s.log.Info("config reloaded")
				}
			}
		}
	}
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
