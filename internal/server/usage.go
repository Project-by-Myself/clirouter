package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/ir"
	"github.com/Project-by-Myself/clirouter/internal/pricing"
	"github.com/Project-by-Myself/clirouter/internal/usage"
)

// loadUsage seeds the usage tracker from the daily rollup.
func (s *Server) loadUsage(kind usage.Kind, name string, since time.Time) usage.Totals {
	if s.store == nil {
		return usage.Totals{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t, err := s.store.UsageSince(ctx, kind == usage.Provider, name, since)
	if err != nil {
		s.log.Warn("load usage", "subject", name, "err", err)
	}
	return usage.Totals{Requests: t.Requests, Input: t.Input, Output: t.Output, Cost: t.Cost}
}

// keyStatus is "active", "disabled", "expired" or "over_budget".
func (s *Server) keyStatus(k *config.ClientKey) string {
	switch {
	case k.Disabled:
		return "disabled"
	case !k.Expiry().IsZero() && time.Now().After(k.Expiry()):
		return "expired"
	case s.h.Usage.OverBudget(usage.Key, k.Name, k.BudgetUSD, k.BudgetTokens):
		return "over_budget"
	}
	return "active"
}

type keyUsage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	usage.Snapshot
}

func (s *Server) adminKeysUsage(w http.ResponseWriter, r *http.Request) {
	cfg := s.h.Runtime().Cfg
	out := make([]keyUsage, 0, len(cfg.APIKeys))
	for i := range cfg.APIKeys {
		k := &cfg.APIKeys[i]
		out = append(out, keyUsage{Name: k.Name, Status: s.keyStatus(k), Snapshot: s.h.Usage.Snapshot(usage.Key, k.Name)})
	}
	if len(cfg.APIKeys) == 0 {
		out = append(out, keyUsage{Name: "local", Status: "active", Snapshot: s.h.Usage.Snapshot(usage.Key, "local")})
	}
	jsonOK(w, out)
}

type providerUsage struct {
	ID         string `json:"id"`
	OverBudget bool   `json:"over_budget"`
	usage.Snapshot
}

func (s *Server) adminProvidersUsage(w http.ResponseWriter, r *http.Request) {
	cfg := s.h.Runtime().Cfg
	out := make([]providerUsage, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		out = append(out, providerUsage{ID: p.ID, OverBudget: s.h.Usage.OverBudget(usage.Provider, p.ID, p.BudgetUSD, 0), Snapshot: s.h.Usage.Snapshot(usage.Provider, p.ID)})
	}
	jsonOK(w, out)
}

func (s *Server) adminUsage(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		jsonErr(w, http.StatusServiceUnavailable, "no store")
		return
	}
	days := map[string]int{"7d": 7, "30d": 30, "90d": 90, "365d": 365}[r.URL.Query().Get("range")]
	if days == 0 {
		days = 30
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "key"
	}
	since := time.Now().AddDate(0, 0, -(days - 1))
	rep, err := s.store.Usage(r.Context(), since, group, r.URL.Query().Get("key"))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, rep)
}

func (s *Server) adminPricing(w http.ResponseWriter, r *http.Request) {
	ov := s.h.Runtime().Cfg.Pricing
	if ov == nil {
		ov = map[string]config.Price{}
	}
	out := map[string]any{"builtin": pricing.Builtin, "overrides": ov}
	if sy := pricing.Current(); sy != nil {
		out["synced"] = map[string]any{"source": sy.Source, "updated_at": sy.UpdatedAt, "count": len(sy.Prices)}
	}
	if m := r.URL.Query().Get("model"); m != "" {
		// Price resolution check: ?model=<upstream model>&provider=<id>
		pr, ok := s.h.Runtime().Pricing.Lookup(r.URL.Query().Get("provider"), m, m)
		out["lookup"] = map[string]any{"model": m, "found": ok, "price": pr}
	}
	jsonOK(w, out)
}

// syncedPath is where the synced price list is cached.
func (s *Server) syncedPath() string {
	return filepath.Join(s.h.Runtime().Cfg.DataDir, "pricing-synced.json")
}

// SyncPricing fetches the configured price list, caches it and installs it.
func (s *Server) SyncPricing(ctx context.Context) (*pricing.Synced, error) {
	sy, err := pricing.Fetch(ctx, s.h.Runtime().Cfg.PricingSync.URL)
	if err != nil {
		return nil, err
	}
	if err := pricing.SaveFile(s.syncedPath(), sy); err != nil {
		s.log.Warn("cache synced prices", "err", err)
	}
	pricing.SetSynced(sy)
	return sy, nil
}

func (s *Server) adminPricingSync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	sy, err := s.SyncPricing(ctx)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "price sync failed: "+err.Error())
		return
	}
	jsonOK(w, map[string]any{"source": sy.Source, "updated_at": sy.UpdatedAt, "count": len(sy.Prices)})
}

// RunPricingSync loads the cached list and, when pricing_sync.auto is set,
// refreshes it daily.
func (s *Server) RunPricingSync(ctx context.Context) {
	if err := pricing.LoadFile(s.syncedPath()); err != nil && !os.IsNotExist(err) {
		s.log.Warn("load synced prices", "err", err)
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if s.h.Runtime().Cfg.PricingSync.Auto {
			if sy := pricing.Current(); sy == nil || time.Since(sy.UpdatedAt) > 24*time.Hour {
				fctx, cancel := context.WithTimeout(ctx, 90*time.Second)
				if _, err := s.SyncPricing(fctx); err != nil {
					s.log.Warn("price sync failed", "err", err)
				}
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type keyLimits struct {
	BudgetUSD    float64 `json:"budget_usd"`
	BudgetTokens int64   `json:"budget_tokens"`
	BudgetPeriod string  `json:"budget_period"`
	RPM          int     `json:"rpm"`
	TPM          int64   `json:"tpm"`
	MaxParallel  int     `json:"max_parallel"`
}

type keyPeriodUsage struct {
	PeriodStart time.Time  `json:"period_start"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
	usage.Totals
}

// keyInfo lets a client key holder see its own limits and usage
// (GET /v1/key/info, authenticated with the client key).
func (s *Server) keyInfo(w http.ResponseWriter, r *http.Request) {
	name, ok := s.clientAuth(w, r, ir.FormatChat)
	if !ok {
		return
	}
	snap := s.h.Usage.Snapshot(usage.Key, name)
	out := map[string]any{
		"name":   name,
		"status": "active",
		"models": []string{},
		"usage":  keyPeriodUsage{PeriodStart: snap.PeriodStart, PeriodEnd: snap.PeriodEnd, Totals: snap.Totals},
		"today":  snap.Today,
		"limits": keyLimits{BudgetPeriod: snap.Period},
	}
	if k := s.h.Runtime().Keys[name]; k != nil {
		out["status"] = s.keyStatus(k)
		if len(k.Models) > 0 {
			out["models"] = k.Models
		}
		if k.Note != "" {
			out["note"] = k.Note
		}
		if k.ExpiresAt != "" {
			out["expires_at"] = k.ExpiresAt
		}
		out["limits"] = keyLimits{BudgetUSD: k.BudgetUSD, BudgetTokens: k.BudgetTokens, BudgetPeriod: config.NormPeriod(k.BudgetPeriod), RPM: k.RPM, TPM: k.TPM, MaxParallel: k.MaxParallel}
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOK(w, out)
}
