package pricing

import (
	"math"
	"testing"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

func TestLookupNormalizes(t *testing.T) {
	tab := New(nil)
	for _, m := range []string{"claude-sonnet-4-5-20250929", "anthropic/claude-sonnet-4-5", "claude-sonnet-4-5@20250929", "Claude-Sonnet-4-5"} {
		if pr, ok := tab.Lookup("x", m, ""); !ok || pr.Input != 3 {
			t.Errorf("%s: %+v %v", m, pr, ok)
		}
	}
	if pr, ok := tab.Lookup("x", "gpt-5-mini-2025-08-07", ""); !ok || pr.Input != 0.25 {
		t.Errorf("gpt-5-mini prefix: %+v", pr)
	}
	if _, ok := tab.Lookup("x", "totally-unknown", "also-unknown"); ok {
		t.Error("unknown model priced")
	}
	// Falls back to the requested model when the upstream id is unknown.
	if pr, ok := tab.Lookup("x", "weird-upstream", "claude-haiku-4-5"); !ok || pr.Input != 1 {
		t.Errorf("requested fallback: %+v", pr)
	}
}

func TestOverridesWin(t *testing.T) {
	tab := New(map[string]config.Price{
		"minimax/*":         {},
		"claude-sonnet-4-5": {Input: 1, Output: 2},
		"or/z-ai/glm-4.6":   {Input: 9, Output: 9},
	})
	if pr, _ := tab.Lookup("minimax", "MiniMax-M2", ""); pr.Input != 0 || pr.Output != 0 {
		t.Errorf("provider wildcard: %+v", pr)
	}
	if pr, _ := tab.Lookup("ant", "claude-sonnet-4-5-20250929", ""); pr.Input != 1 {
		t.Errorf("model override: %+v", pr)
	}
	if pr, _ := tab.Lookup("or", "z-ai/glm-4.6", ""); pr.Input != 9 {
		t.Errorf("provider/model override: %+v", pr)
	}
}

func TestCostMath(t *testing.T) {
	cr, cw := 0.3, 3.75
	pr := config.Price{Input: 3, Output: 15, CacheRead: &cr, CacheWrite: &cw}
	// 10k input of which 6k cache read and 1k cache write, 2k output.
	got := Of(pr, Usage{Input: 10000, Output: 2000, CacheRead: 6000, CacheWrite: 1000})
	want := (3000*3 + 6000*0.3 + 1000*3.75 + 2000*15) / 1e6
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("got %v want %v", got, want)
	}
	// Unset cache prices fall back to the input price.
	if got := Of(config.Price{Input: 1, Output: 1}, Usage{Input: 100, CacheRead: 100}); math.Abs(got-100.0/1e6) > 1e-12 {
		t.Fatalf("fallback: %v", got)
	}
}

func TestParseLiteLLMAndSyncedLayer(t *testing.T) {
	raw := []byte(`{
	  "sample_spec": {"input_cost_per_token": 1},
	  "claude-sonnet-4-5-20250929": {"input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07, "litellm_provider": "anthropic", "mode": "chat"},
	  "us.anthropic.claude-sonnet-4-5-20250929-v1:0": {"input_cost_per_token": 9e-06, "output_cost_per_token": 9e-05, "litellm_provider": "bedrock_converse", "mode": "chat"},
	  "openrouter/new-vendor/shiny-model": {"input_cost_per_token": 2e-06, "output_cost_per_token": 4e-06, "litellm_provider": "openrouter", "mode": "chat"},
	  "text-embedding-3-small": {"input_cost_per_token": 2e-08, "output_cost_per_token": 0, "litellm_provider": "openai", "mode": "embedding"}
	}`)
	got, err := ParseLiteLLM(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p := got["claude-sonnet-4-5"]; math.Abs(p.Input-3) > 1e-9 || p.CacheRead == nil || math.Abs(*p.CacheRead-0.3) > 1e-9 {
		t.Fatalf("first-party price should win: %+v", p)
	}
	if _, ok := got["text-embedding-3-small"]; ok {
		t.Fatal("embedding models must be skipped")
	}
	SetSynced(&Synced{Prices: got})
	defer SetSynced(nil)
	tab := New(map[string]config.Price{"shiny-model": {Input: 1, Output: 1}})
	if pr, _ := tab.Lookup("x", "shiny-model", ""); pr.Input != 1 {
		t.Fatalf("override must beat synced: %+v", pr)
	}
	if pr, ok := New(nil).Lookup("or", "new-vendor/shiny-model", ""); !ok || math.Abs(pr.Input-2) > 1e-9 {
		t.Fatalf("synced model not found: %+v %v", pr, ok)
	}
}
