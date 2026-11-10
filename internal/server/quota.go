package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/redact"
	"github.com/Project-by-Myself/clirouter/internal/router"
)

// QuotaPollInterval is how often subscription usage endpoints are polled.
var QuotaPollInterval = 5 * time.Minute

// refreshQuota fetches usage windows for one OAuth account.
func (s *Server) refreshQuota(ctx context.Context, a *router.Account) {
	if s.QuotaFetch == nil || a.OAuth == nil {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, err := s.QuotaFetch(fctx, a.Label)
	if err != nil {
		err = errors.New(redact.String(err.Error()))
	}
	if err == nil && len(ws) == 0 {
		return // no usage endpoint for this account type
	}
	a.SetQuota(ws, err)
}

// RunQuotaPoll polls subscription usage for OAuth accounts until ctx ends.
func (s *Server) RunQuotaPoll(ctx context.Context) {
	if s.QuotaFetch == nil {
		return
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		for _, p := range s.h.Runtime().Router.Providers() {
			for _, a := range p.Accounts {
				s.refreshQuota(ctx, a)
			}
		}
		timer.Reset(QuotaPollInterval)
	}
}

func (s *Server) adminRefreshQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, p := range s.h.Runtime().Router.Providers() {
		for _, a := range p.Accounts {
			if a.ID == id {
				if a.OAuth == nil {
					jsonErr(w, http.StatusBadRequest, "quota can only be fetched for subscription (OAuth) accounts; API-key accounts report quota with each response")
					return
				}
				s.refreshQuota(r.Context(), a)
				jsonOK(w, a.Status())
				return
			}
		}
	}
	jsonErr(w, http.StatusNotFound, "no such account")
}
