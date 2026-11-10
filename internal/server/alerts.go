package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/alerts"
	"github.com/Project-by-Myself/clirouter/internal/config"
)

// applyAlerts pushes the alert settings to the notifier and marks budget
// thresholds that are already crossed, so a restart or config edit does not
// re-announce them.
func (s *Server) applyAlerts(cfg *config.Config) {
	s.h.Alerts.SetConfig(cfg.Alerts)
	rt := s.h.Runtime()
	for i := range cfg.APIKeys {
		s.h.BudgetAlerts(rt, cfg.APIKeys[i].Name, "", true)
	}
	for _, p := range cfg.Providers {
		s.h.BudgetAlerts(rt, "", p.ID, true)
	}
}

// RunAlerts delivers alerts and periodically checks key expiry and upstream
// quota until ctx ends.
func (s *Server) RunAlerts(ctx context.Context) {
	go s.h.Alerts.Run(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.checkPeriodic(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) checkPeriodic(now time.Time) {
	rt := s.h.Runtime()
	a := rt.Cfg.Alerts
	if !a.Enabled() {
		return
	}
	if days := a.ExpiryDays; days >= 0 {
		if days == 0 {
			days = 3
		}
		for _, k := range rt.Cfg.APIKeys {
			exp := k.Expiry()
			if exp.IsZero() || k.Disabled || exp.Before(now) || exp.Sub(now) > time.Duration(days)*24*time.Hour {
				continue
			}
			s.h.Alerts.Once("expiry|"+k.Name+"|"+k.ExpiresAt, alertEvent("key_expiring", k.Name, 0,
				fmt.Sprintf("⏳ clirouter: key %q expires on %s (in %s).", k.Name, exp.Format("2006-01-02 15:04"), humanDur(exp.Sub(now)))))
		}
	}
	if lim := a.QuotaPct; lim >= 0 {
		if lim == 0 {
			lim = 90
		}
		for _, p := range rt.Router.Providers() {
			for _, acct := range p.Accounts {
				st := acct.Status()
				for _, w := range st.Quota {
					if w.UsedPct == nil || *w.UsedPct < float64(lim) {
						continue
					}
					reset := ""
					if w.ResetsAt != nil {
						reset = w.ResetsAt.Format(time.RFC3339)
					}
					// One alert per window instance (its reset time identifies it).
					key := "quota|" + st.ID + "|" + w.Name + "|" + reset
					text := fmt.Sprintf("📉 clirouter: %s account %s has used %.0f%% of its %s quota.", st.Provider, st.Label, *w.UsedPct, w.Name)
					if w.ResetsAt != nil {
						text += " Resets in " + humanDur(w.ResetsAt.Sub(now)) + "."
					}
					s.h.Alerts.Once(key, alertEvent("quota", st.Provider+"/"+st.Label, lim, text))
				}
			}
		}
	}
}

func humanDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

func (s *Server) adminTestAlert(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if errs := s.h.Alerts.Test(ctx); len(errs) > 0 {
		jsonErr(w, http.StatusBadGateway, fmt.Sprint(errs))
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func alertEvent(kind, subject string, threshold int, text string) alerts.Event {
	return alerts.Event{Kind: kind, Subject: subject, Threshold: threshold, Text: text}
}
