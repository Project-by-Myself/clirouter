package router

import (
	"net/http"
	"testing"
	"time"
)

func TestParseQuota(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "5000")
	h.Set("x-ratelimit-remaining-requests", "4990")
	h.Set("x-ratelimit-reset-requests", "12ms")
	h.Set("x-codex-primary-used-percent", "34")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-primary-reset-after-seconds", "600")
	h.Set("x-codex-secondary-used-percent", "12.5")
	h.Set("x-codex-secondary-window-minutes", "10080")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.5")
	h.Set("anthropic-ratelimit-unified-7d-reset", "1800003600")
	ws := parseQuota(h, now)
	byName := map[string]QuotaWindow{}
	for _, w := range ws {
		byName[w.Name] = w
	}
	if w := byName["requests"]; w.Remaining == nil || *w.Remaining != 4990 || w.UsedPct == nil || *w.UsedPct < 0.19 || *w.UsedPct > 0.21 {
		t.Errorf("openai requests: %+v", w)
	}
	if w := byName["5h"]; w.UsedPct == nil || *w.UsedPct != 34 || w.ResetsAt == nil || !w.ResetsAt.Equal(now.Add(10*time.Minute)) {
		t.Errorf("codex 5h: %+v", w)
	}
	if w, ok := byName["7d"]; !ok || w.UsedPct == nil {
		t.Errorf("7d window missing: %+v", ws)
	}
	if len(parseQuota(http.Header{"Content-Type": {"x"}}, now)) != 0 {
		t.Error("no quota headers should yield nothing")
	}
}
