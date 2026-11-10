package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/router"
)

// Usage endpoints the official clients use to show subscription limits.
// Undocumented; responses are parsed defensively and may change.
var (
	ClaudeUsageURL     = "https://api.anthropic.com/api/oauth/usage"
	CodexUsageURL      = "https://chatgpt.com/backend-api/wham/usage"
	CopilotUserURL     = "https://api.github.com/copilot_internal/user"
	GeminiQuotaURLPath = "/v1internal:retrieveUserQuota"
)

// FetchQuota asks the provider how much of the account's subscription
// windows is used. It returns no windows (and no error) for account types
// without a known usage endpoint.
func (m *Manager) FetchQuota(ctx context.Context, accountID string) ([]router.QuotaWindow, error) {
	s := m.source(accountID)
	tok, err := s.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	var c Credential
	if s.cred != nil {
		c = *s.cred
	}
	s.mu.Unlock()
	now := time.Now()
	var raw json.RawMessage
	switch c.Type {
	case TypeClaude:
		err = m.doJSON(ctx, http.MethodGet, ClaudeUsageURL, nil, &raw,
			withHeader("Authorization", "Bearer "+tok), withHeader("anthropic-beta", "oauth-2025-04-20"))
		if err != nil {
			return nil, fmt.Errorf("claude usage: %w", err)
		}
		return parseClaudeUsage(raw, now), nil
	case TypeCodex:
		opts := []reqOpt{withHeader("Authorization", "Bearer "+tok)}
		if c.AccountID != "" {
			opts = append(opts, withHeader("chatgpt-account-id", c.AccountID))
		}
		if err = m.doJSON(ctx, http.MethodGet, CodexUsageURL, nil, &raw, opts...); err != nil {
			return nil, fmt.Errorf("codex usage: %w", err)
		}
		return parseCodexUsage(raw, now), nil
	case TypeCopilot:
		if c.RefreshToken == "" {
			return nil, nil
		}
		if err = m.doJSON(ctx, http.MethodGet, CopilotUserURL, nil, &raw, withHeader("Authorization", "token "+c.RefreshToken)); err != nil {
			return nil, fmt.Errorf("copilot usage: %w", err)
		}
		return parseCopilotUsage(raw, now), nil
	case TypeGeminiCLI:
		body := map[string]string{}
		if c.ProjectID != "" {
			body["project"] = c.ProjectID
		}
		if err = m.doJSON(ctx, http.MethodPost, CodeAssistBaseURL+GeminiQuotaURLPath, body, &raw, withHeader("Authorization", "Bearer "+tok)); err != nil {
			return nil, fmt.Errorf("gemini quota: %w", err)
		}
		return parseGeminiQuota(raw, now), nil
	}
	return nil, nil
}

func pct(f float64) *float64 { return &f }

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func resetOf(m map[string]any, now time.Time, keys ...string) *time.Time {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if t, ok := router.ParseReset(v, now); ok {
				return &t
			}
		case float64:
			var t time.Time
			if strings.Contains(k, "after") || strings.Contains(k, "in_seconds") {
				t = now.Add(time.Duration(v) * time.Second)
			} else if v > 1e9 {
				t = time.Unix(int64(v), 0)
			} else {
				continue
			}
			return &t
		}
	}
	return nil
}

// parseClaudeUsage reads {"five_hour":{"utilization":34,"resets_at":"…"},"seven_day":{…},…}.
func parseClaudeUsage(raw json.RawMessage, now time.Time) []router.QuotaWindow {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	names := map[string]string{"five_hour": "5h", "seven_day": "7d", "seven_day_opus": "7d opus", "seven_day_sonnet": "7d sonnet", "seven_day_oauth_apps": "7d apps"}
	var out []router.QuotaWindow
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		obj, ok := m[k].(map[string]any)
		if !ok {
			continue
		}
		u, ok := num(obj["utilization"])
		if !ok {
			continue
		}
		// utilization is a percentage (0..100), as the official client shows it.
		name := names[k]
		if name == "" {
			name = strings.ReplaceAll(k, "_", " ")
		}
		out = append(out, router.QuotaWindow{Name: name, UsedPct: pct(u), ResetsAt: resetOf(obj, now, "resets_at", "reset_at")})
	}
	return out
}

// parseCodexUsage finds every object carrying "used_percent" (primary and
// secondary windows) and names it by its window length.
func parseCodexUsage(raw json.RawMessage, now time.Time) []router.QuotaWindow {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	var out []router.QuotaWindow
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if u, ok := num(x["used_percent"]); ok {
				name := path
				if secs, ok := num(x["limit_window_seconds"]); ok && secs > 0 {
					name = router.WindowName(int64(secs) / 60)
				} else if mins, ok := num(x["window_minutes"]); ok && mins > 0 {
					name = router.WindowName(int64(mins))
				}
				out = append(out, router.QuotaWindow{Name: name, UsedPct: pct(u), ResetsAt: resetOf(x, now, "reset_after_seconds", "resets_in_seconds", "reset_at", "resets_at")})
				return
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(strings.TrimSuffix(k, "_window"), x[k])
			}
		case []any:
			for _, e := range x {
				walk(path, e)
			}
		}
	}
	walk("codex", root)
	return out
}

// parseCopilotUsage reads quota_snapshots (chat, completions, premium_interactions).
func parseCopilotUsage(raw json.RawMessage, now time.Time) []router.QuotaWindow {
	var m struct {
		Snapshots map[string]map[string]any `json:"quota_snapshots"`
		ResetDate string                    `json:"quota_reset_date"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var reset *time.Time
	if t, err := time.Parse("2006-01-02", m.ResetDate); err == nil {
		reset = &t
	} else if t, ok := router.ParseReset(m.ResetDate, now); ok {
		reset = &t
	}
	keys := make([]string, 0, len(m.Snapshots))
	for k := range m.Snapshots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []router.QuotaWindow
	for _, k := range keys {
		s := m.Snapshots[k]
		if unl, _ := s["unlimited"].(bool); unl {
			continue
		}
		w := router.QuotaWindow{Name: strings.ReplaceAll(k, "_", " "), ResetsAt: reset}
		if p, ok := num(s["percent_remaining"]); ok {
			w.UsedPct = pct(100 - p)
		}
		if r, ok := num(s["remaining"]); ok {
			n := int64(r)
			w.Remaining = &n
		}
		if e, ok := num(s["entitlement"]); ok {
			n := int64(e)
			w.Limit = &n
		}
		if w.UsedPct == nil && w.Remaining == nil {
			continue
		}
		out = append(out, w)
	}
	return out
}

// parseGeminiQuota reads {"buckets":[{"modelId":…,"tokenType":…,"remainingFraction":0.8,"resetTime":"…"}]}.
func parseGeminiQuota(raw json.RawMessage, now time.Time) []router.QuotaWindow {
	var m struct {
		Buckets []map[string]any `json:"buckets"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var out []router.QuotaWindow
	for _, b := range m.Buckets {
		f, ok := num(b["remainingFraction"])
		if !ok {
			continue
		}
		name, _ := b["modelId"].(string)
		if tt, _ := b["tokenType"].(string); tt != "" && tt != "REQUESTS" {
			name += " " + strings.ToLower(tt)
		}
		if name == "" {
			name = "daily"
		}
		out = append(out, router.QuotaWindow{Name: name, UsedPct: pct(100 * (1 - f)), ResetsAt: resetOf(b, now, "resetTime")})
	}
	return out
}
