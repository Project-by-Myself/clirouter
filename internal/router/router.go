// Package router resolves a requested model into an ordered list of upstream
// targets and picks a healthy account for each attempt. All routing state is
// in memory and guarded per account, so the request path never waits on disk
// or on a process-wide lock.
package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/cloudauth"
	"github.com/Project-by-Myself/clirouter/internal/config"
)

type Provider struct {
	Cfg      config.Provider
	Accounts []*Account
	rr       atomic.Uint64
	models   map[string]bool
}

type Target struct {
	Provider *Provider
	Model    string // upstream model id
}

type Router struct {
	providers map[string]*Provider
	ordered   []*Provider
	combos    map[string]config.Combo
	comboRR   map[string]*atomic.Uint64
	aliases   map[string]string
	strategy  string
	affinity  *affinityCache
}

// TokenSource supplies OAuth bearer tokens; nil for API-key accounts.
type TokenSource interface {
	Token() (string, error)
	Refresh() error
	Meta() map[string]string
}

// New builds a router. prev (may be nil) donates account health state so a
// config reload does not forget cooldowns.
func New(cfg *config.Config, prev *Router, oauth func(providerID, accountID string) TokenSource) *Router {
	r := &Router{
		providers: map[string]*Provider{},
		combos:    map[string]config.Combo{},
		comboRR:   map[string]*atomic.Uint64{},
		aliases:   cfg.Aliases,
		strategy:  cfg.Routing.Strategy,
	}
	if prev != nil {
		r.affinity = prev.affinity
	} else {
		r.affinity = newAffinityCache(20000, 2*time.Hour)
	}
	old := map[string]*Account{}
	if prev != nil {
		for _, p := range prev.ordered {
			for _, a := range p.Accounts {
				old[a.ID] = a
			}
		}
	}
	for _, pc := range cfg.Providers {
		if pc.Disabled {
			continue
		}
		p := &Provider{Cfg: pc, models: map[string]bool{}}
		for _, m := range pc.Models {
			p.models[m] = true
		}
		for m := range pc.ModelMap {
			p.models[m] = true
		}
		for _, k := range pc.Keys {
			key := config.Secret(k)
			if key == "" {
				continue
			}
			sum := sha256.Sum256([]byte(key))
			a := &Account{ID: pc.ID + ":" + hex.EncodeToString(sum[:5]), Label: mask(key), Provider: p, apiKey: key}
			if ref, ok := strings.CutPrefix(key, config.ServiceAccountPrefix); ok && pc.Type == "vertex" {
				// A service-account key is a file path or env reference, not
				// a secret; tokens come from the shared OAuth2 JWT flow.
				a.Label, a.apiKey = key, ""
				a.OAuth = cloudauth.SharedServiceAccount(ref, pc.Proxy)
			}
			if o := old[a.ID]; o != nil {
				a.st = o.st
			} else {
				a.st = newState()
			}
			p.Accounts = append(p.Accounts, a)
		}
		if len(p.Accounts) == 0 && len(pc.OAuth) == 0 && config.KeyOptional(pc.Type) {
			// Local servers (ollama) need no key: one keyless account.
			a := &Account{ID: pc.ID + ":local", Label: "no key", Provider: p}
			if o := old[a.ID]; o != nil {
				a.st = o.st
			} else {
				a.st = newState()
			}
			p.Accounts = append(p.Accounts, a)
		}
		for _, id := range pc.OAuth {
			a := &Account{ID: pc.ID + ":" + id, Label: id, Provider: p}
			if oauth != nil {
				a.OAuth = oauth(pc.ID, id)
			}
			if o := old[a.ID]; o != nil {
				a.st = o.st
			} else {
				a.st = newState()
			}
			p.Accounts = append(p.Accounts, a)
		}
		r.providers[pc.ID] = p
		r.ordered = append(r.ordered, p)
	}
	slices.SortStableFunc(r.ordered, func(a, b *Provider) int { return a.Cfg.Priority - b.Cfg.Priority })
	for _, c := range cfg.Combos {
		r.combos[c.Name] = c
		r.comboRR[c.Name] = &atomic.Uint64{}
	}
	return r
}

func mask(k string) string {
	if len(k) < 16 {
		return "****"
	}
	return "…" + k[len(k)-4:]
}

func (r *Router) Providers() []*Provider { return r.ordered }

func (r *Router) Provider(id string) *Provider { return r.providers[id] }

// Resolve maps a client model name to ordered targets.
func (r *Router) Resolve(model string) ([]Target, error) {
	return r.resolve(model, 0)
}

func (r *Router) resolve(model string, depth int) ([]Target, error) {
	if depth > 4 {
		return nil, fmt.Errorf("alias loop at %q", model)
	}
	if a, ok := r.aliases[model]; ok && a != model {
		return r.resolve(a, depth+1)
	}
	if c, ok := r.combos[model]; ok {
		targets := c.Targets
		if c.Strategy == "round-robin" && len(targets) > 1 {
			n := int(r.comboRR[c.Name].Add(1)-1) % len(targets)
			targets = append(slices.Clone(targets[n:]), targets[:n]...)
		}
		var out []Target
		for _, t := range targets {
			ts, err := r.resolve(t, depth+1)
			if err == nil {
				out = append(out, ts...)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("combo %q has no usable targets", model)
		}
		return out, nil
	}
	if pid, m, ok := strings.Cut(model, "/"); ok {
		if p := r.providers[pid]; p != nil && len(p.Accounts) > 0 {
			return []Target{{Provider: p, Model: p.upstreamModel(m)}}, nil
		}
	}
	var out []Target
	for _, p := range r.ordered {
		if len(p.Accounts) > 0 && p.models[model] {
			out = append(out, Target{Provider: p, Model: p.upstreamModel(model)})
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	// No explicit listing: fall back to providers that accept any model of
	// the matching family.
	fam := family(model)
	for _, p := range r.ordered {
		if len(p.Accounts) > 0 && len(p.models) == 0 && typeFamily(p.Cfg.Type) == fam {
			out = append(out, Target{Provider: p, Model: model})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no provider serves model %q", model)
	}
	return out, nil
}

func (p *Provider) upstreamModel(m string) string {
	if v, ok := p.Cfg.ModelMap[m]; ok && v != "" {
		return v
	}
	return m
}

func family(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gemini"):
		return "gemini"
	case strings.HasPrefix(m, "gpt"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"), strings.HasPrefix(m, "codex"), strings.HasPrefix(m, "chatgpt"):
		return "openai"
	}
	return "other"
}

func typeFamily(t string) string {
	switch t {
	case "anthropic", "claude-oauth":
		return "anthropic"
	case "gemini", "vertex", "gemini-cli":
		return "gemini"
	case "openai", "codex", "azure-openai":
		return "openai"
	}
	return "other" // openai-compatible, ollama, bedrock (model ids are provider-specific)
}

// Pick chooses an account of t's provider. When every account is cooling
// down it returns nil and the shortest remaining wait.
func (r *Router) Pick(t Target, sessionKey string, exclude map[*Account]bool) (*Account, time.Duration) {
	now := time.Now()
	accts := t.Provider.Accounts
	var minWait time.Duration = -1
	avail := make([]*Account, 0, len(accts))
	for _, a := range accts {
		if exclude[a] {
			continue
		}
		if w := a.st.wait(t.Model, now); w > 0 {
			if minWait < 0 || w < minWait {
				minWait = w
			}
			continue
		}
		avail = append(avail, a)
	}
	if len(avail) == 0 {
		return nil, max(minWait, 0)
	}
	if sessionKey != "" {
		if id, ok := r.affinity.get(sessionKey); ok {
			for _, a := range avail {
				if a.ID == id {
					return a, 0
				}
			}
		}
	}
	var pick *Account
	switch r.strategy {
	case "fill-first":
		pick = avail[0]
	case "least-busy":
		pick = avail[0]
		for _, a := range avail[1:] {
			if a.inflight.Load() < pick.inflight.Load() {
				pick = a
			}
		}
	default:
		pick = avail[int(t.Provider.rr.Add(1)-1)%len(avail)]
	}
	return pick, 0
}

// Bind records which account served a session so follow-up turns reuse it
// and keep the upstream prompt cache warm.
func (r *Router) Bind(sessionKey string, a *Account) {
	if sessionKey != "" {
		r.affinity.put(sessionKey, a.ID)
	}
}

type affinityCache struct {
	mu  sync.Mutex
	max int
	ttl time.Duration
	m   map[string]affEntry
}

type affEntry struct {
	id string
	at time.Time
}

func newAffinityCache(max int, ttl time.Duration) *affinityCache {
	return &affinityCache{max: max, ttl: ttl, m: map[string]affEntry{}}
}

func (c *affinityCache) get(k string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Since(e.at) > c.ttl {
		return "", false
	}
	return e.id, true
}

func (c *affinityCache) put(k, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.m) >= c.max {
		for key, e := range c.m {
			if now.Sub(e.at) > c.ttl || len(c.m) >= c.max {
				delete(c.m, key)
			}
			if len(c.m) < c.max*9/10 {
				break
			}
		}
	}
	c.m[k] = affEntry{id: id, at: now}
}
