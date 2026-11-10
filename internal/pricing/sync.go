package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

// DefaultSyncURL is LiteLLM's community-maintained price list.
const DefaultSyncURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// Synced is a price list fetched from an external source. It sits between
// user overrides and the built-in table.
type Synced struct {
	Source    string                  `json:"source"`
	UpdatedAt time.Time               `json:"updated_at"`
	Prices    map[string]config.Price `json:"prices"`
}

var synced atomic.Pointer[Synced]

// Current returns the synced list, or nil.
func Current() *Synced { return synced.Load() }

// SetSynced installs a synced list (nil clears it).
func SetSynced(s *Synced) { synced.Store(s) }

// LoadFile installs a previously saved synced list.
func LoadFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s Synced
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	SetSynced(&s)
	return nil
}

// SaveFile writes s atomically.
func SaveFile(path string, s *Synced) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Fetch downloads and parses a LiteLLM-format price list.
func Fetch(ctx context.Context, url string) (*Synced, error) {
	if url == "" {
		url = DefaultSyncURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "clirouter-pricing-sync")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	prices, err := ParseLiteLLM(b)
	if err != nil {
		return nil, err
	}
	return &Synced{Source: url, UpdatedAt: time.Now(), Prices: prices}, nil
}

type litellmEntry struct {
	Input      *float64 `json:"input_cost_per_token"`
	Output     *float64 `json:"output_cost_per_token"`
	CacheRead  *float64 `json:"cache_read_input_token_cost"`
	CacheWrite *float64 `json:"cache_creation_input_token_cost"`
	Provider   string   `json:"litellm_provider"`
	Mode       string   `json:"mode"`
}

var (
	regionPrefix  = regexp.MustCompile(`^(us|eu|apac|au|jp|global)\.`)
	vendorPrefix  = regexp.MustCompile(`^(anthropic|meta|amazon|mistral|cohere|ai21|deepseek|openai|qwen|writer|twelvelabs)\.`)
	bedrockSuffix = regexp.MustCompile(`-v\d+(:\d+)?$`)
)

// syncName maps a LiteLLM key ("anthropic.claude-…-v1:0", "gemini/gemini-2.5-pro")
// to the normalized name Lookup uses.
func syncName(k string) string {
	k = strings.ToLower(k)
	if i := strings.LastIndexByte(k, '/'); i >= 0 {
		k = k[i+1:]
	}
	k = regionPrefix.ReplaceAllString(k, "")
	k = vendorPrefix.ReplaceAllString(k, "")
	k = bedrockSuffix.ReplaceAllString(k, "")
	return normalize(k)
}

// rank prefers first-party list prices over resellers and clouds.
func rank(provider string) int {
	switch provider {
	case "anthropic", "openai", "gemini", "vertex_ai-language-models", "deepseek", "xai", "mistral", "minimax", "zai", "moonshot", "dashscope":
		return 0
	case "openrouter":
		return 2
	}
	return 1
}

// ParseLiteLLM converts LiteLLM's per-token prices to per-1M-token prices
// keyed by normalized model name.
func ParseLiteLLM(b []byte) (map[string]config.Price, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse price list: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic tie-breaking
	out := map[string]config.Price{}
	best := map[string]int{}
	per1M := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		x := *v * 1e6
		return &x
	}
	for _, k := range keys {
		if k == "sample_spec" {
			continue
		}
		var e litellmEntry
		if json.Unmarshal(raw[k], &e) != nil || e.Input == nil || e.Output == nil {
			continue
		}
		if e.Mode != "" && e.Mode != "chat" && e.Mode != "responses" && e.Mode != "completion" {
			continue
		}
		name := syncName(k)
		if name == "" {
			continue
		}
		r := rank(e.Provider)
		if prev, ok := best[name]; ok && prev <= r {
			continue
		}
		best[name] = r
		out[name] = config.Price{Input: *e.Input * 1e6, Output: *e.Output * 1e6, CacheRead: per1M(e.CacheRead), CacheWrite: per1M(e.CacheWrite)}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("price list has no chat models")
	}
	return out, nil
}
