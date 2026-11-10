package router

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QuotaWindow is one upstream rate-limit or usage window as reported in
// response headers (Anthropic, OpenAI, Codex subscription windows, …).
type QuotaWindow struct {
	Name      string     `json:"name"`
	UsedPct   *float64   `json:"used_pct,omitempty"`
	Remaining *int64     `json:"remaining,omitempty"`
	Limit     *int64     `json:"limit,omitempty"`
	ResetsAt  *time.Time `json:"resets_at,omitempty"`
}

// ObserveQuota records the quota headers of an upstream response. It is
// cheap when no such headers are present.
func (a *Account) ObserveQuota(h http.Header) {
	if len(h) == 0 {
		return
	}
	ws := parseQuota(h, time.Now())
	if len(ws) == 0 {
		return
	}
	s := a.st
	s.mu.Lock()
	s.quota, s.quotaAt = ws, time.Now()
	s.mu.Unlock()
}

func parseQuota(h http.Header, now time.Time) []QuotaWindow {
	get := func(k string) string { return h.Get(k) }
	var out []QuotaWindow
	num := func(v string) *int64 {
		if v == "" {
			return nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	pct := func(f float64) *float64 { return &f }

	// Anthropic API keys: anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-{limit,remaining,reset}.
	for _, n := range []string{"requests", "tokens", "input-tokens", "output-tokens"} {
		lim, rem := num(get("anthropic-ratelimit-"+n+"-limit")), num(get("anthropic-ratelimit-"+n+"-remaining"))
		if lim == nil && rem == nil {
			continue
		}
		w := QuotaWindow{Name: n, Limit: lim, Remaining: rem}
		if t, err := time.Parse(time.RFC3339, get("anthropic-ratelimit-"+n+"-reset")); err == nil {
			w.ResetsAt = &t
		}
		fillPct(&w)
		out = append(out, w)
	}
	// OpenAI-style: x-ratelimit-{limit,remaining,reset}-{requests,tokens}; reset is a duration ("6m0s", "20ms").
	for _, n := range []string{"requests", "tokens"} {
		lim, rem := num(get("x-ratelimit-limit-"+n)), num(get("x-ratelimit-remaining-"+n))
		if lim == nil && rem == nil {
			continue
		}
		w := QuotaWindow{Name: n, Limit: lim, Remaining: rem}
		if d, ok := parseReset(get("x-ratelimit-reset-"+n), now); ok {
			w.ResetsAt = &d
		}
		fillPct(&w)
		out = append(out, w)
	}
	// Scans for windows whose names are not fixed.
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		// Anthropic subscription (unified) windows: anthropic-ratelimit-unified-<w>-utilization (0..1), -reset (unix s).
		if rest, ok := strings.CutPrefix(k, "anthropic-ratelimit-unified-"); ok {
			if win, ok := strings.CutSuffix(rest, "-utilization"); ok {
				f, err := strconv.ParseFloat(get(k), 64)
				if err != nil {
					continue
				}
				if f <= 1 {
					f *= 100
				}
				w := QuotaWindow{Name: win, UsedPct: pct(f)}
				if t, ok := parseReset(get("anthropic-ratelimit-unified-"+win+"-reset"), now); ok {
					w.ResetsAt = &t
				}
				out = append(out, w)
			}
			continue
		}
		// Codex subscription windows: x-codex-{primary,secondary}-used-percent, -window-minutes, -reset-after-seconds | -reset-at.
		if rest, ok := strings.CutPrefix(k, "x-codex-"); ok {
			if win, ok := strings.CutSuffix(rest, "-used-percent"); ok {
				f, err := strconv.ParseFloat(get(k), 64)
				if err != nil {
					continue
				}
				name := win
				if m := num(get("x-codex-" + win + "-window-minutes")); m != nil && *m > 0 {
					name = windowName(*m)
				}
				w := QuotaWindow{Name: name, UsedPct: pct(f)}
				if s := num(get("x-codex-" + win + "-reset-after-seconds")); s != nil {
					t := now.Add(time.Duration(*s) * time.Second)
					w.ResetsAt = &t
				} else if t, ok := parseReset(get("x-codex-"+win+"-reset-at"), now); ok {
					w.ResetsAt = &t
				}
				out = append(out, w)
			}
		}
	}
	return out
}

func fillPct(w *QuotaWindow) {
	if w.Limit != nil && w.Remaining != nil && *w.Limit > 0 {
		f := 100 * float64(*w.Limit-*w.Remaining) / float64(*w.Limit)
		w.UsedPct = &f
	}
}

// windowName renders a window length: 300 → "5h", 10080 → "7d".
func windowName(min int64) string {
	switch {
	case min%1440 == 0:
		return strconv.FormatInt(min/1440, 10) + "d"
	case min%60 == 0:
		return strconv.FormatInt(min/60, 10) + "h"
	default:
		return strconv.FormatInt(min, 10) + "m"
	}
}

// parseReset accepts RFC3339, unix seconds, or a Go/OpenAI duration.
func parseReset(v string, now time.Time) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n > 1e9 {
			return time.Unix(n, 0), true
		}
		return now.Add(time.Duration(n) * time.Second), true
	}
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(d), true
	}
	return time.Time{}, false
}
