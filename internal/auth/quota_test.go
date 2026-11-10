package auth

import (
	"testing"
	"time"
)

func TestParseUsageResponses(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cl := parseClaudeUsage([]byte(`{"five_hour":{"utilization":34.0,"resets_at":"2027-01-15T10:00:00Z"},"seven_day":{"utilization":12,"resets_at":"2027-01-20T00:00:00Z"},"seven_day_opus":null,"extra_usage":{"is_enabled":false}}`), now)
	if len(cl) != 2 || cl[0].Name != "5h" || *cl[0].UsedPct != 34 || cl[0].ResetsAt == nil || cl[1].Name != "7d" {
		t.Fatalf("claude: %+v", cl)
	}
	cx := parseCodexUsage([]byte(`{"plan_type":"plus","rate_limit":{"allowed":true,"primary_window":{"used_percent":41,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":7,"limit_window_seconds":604800,"reset_at":1800600000}}}`), now)
	if len(cx) != 2 || cx[0].Name != "5h" || *cx[0].UsedPct != 41 || !cx[0].ResetsAt.Equal(now.Add(time.Hour)) || cx[1].Name != "7d" {
		t.Fatalf("codex: %+v", cx)
	}
	cp := parseCopilotUsage([]byte(`{"quota_reset_date":"2027-02-01","quota_snapshots":{"chat":{"unlimited":true},"premium_interactions":{"entitlement":300,"remaining":120,"percent_remaining":40,"unlimited":false}}}`), now)
	if len(cp) != 1 || cp[0].Name != "premium interactions" || *cp[0].UsedPct != 60 || *cp[0].Remaining != 120 || *cp[0].Limit != 300 {
		t.Fatalf("copilot: %+v", cp)
	}
	gm := parseGeminiQuota([]byte(`{"buckets":[{"modelId":"gemini-2.5-pro","tokenType":"REQUESTS","remainingFraction":0.75,"resetTime":"2027-01-16T00:00:00Z"}]}`), now)
	if len(gm) != 1 || gm[0].Name != "gemini-2.5-pro" || *gm[0].UsedPct != 25 {
		t.Fatalf("gemini: %+v", gm)
	}
}
