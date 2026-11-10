// Package pricing estimates request cost from token usage. Prices are public
// list prices in USD per 1M tokens and only an estimate: flat-rate plans,
// discounts, long-context tiers and batch pricing are not modelled. Users
// override or zero them in config (pricing:).
package pricing

import (
	"regexp"
	"strings"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

func p(in, out, cacheRead, cacheWrite float64) config.Price {
	pr := config.Price{Input: in, Output: out}
	if cacheRead >= 0 {
		pr.CacheRead = &cacheRead
	}
	if cacheWrite >= 0 {
		pr.CacheWrite = &cacheWrite
	}
	return pr
}

// Builtin holds list prices for common models (-1 = not published; falls
// back to the input price).
var Builtin = map[string]config.Price{
	// Anthropic
	"claude-opus-4-5":   p(5, 25, 0.5, 6.25),
	"claude-opus-4-1":   p(15, 75, 1.5, 18.75),
	"claude-opus-4":     p(15, 75, 1.5, 18.75),
	"claude-sonnet-4-5": p(3, 15, 0.3, 3.75),
	"claude-sonnet-4":   p(3, 15, 0.3, 3.75),
	"claude-3-7-sonnet": p(3, 15, 0.3, 3.75),
	"claude-haiku-4-5":  p(1, 5, 0.1, 1.25),
	"claude-3-5-haiku":  p(0.8, 4, 0.08, 1),
	// OpenAI (cached input is the cache-read price; no cache-write charge)
	"gpt-5":        p(1.25, 10, 0.125, 0),
	"gpt-5-codex":  p(1.25, 10, 0.125, 0),
	"gpt-5.1":      p(1.25, 10, 0.125, 0),
	"gpt-5-mini":   p(0.25, 2, 0.025, 0),
	"gpt-5-nano":   p(0.05, 0.4, 0.005, 0),
	"gpt-4.1":      p(2, 8, 0.5, 0),
	"gpt-4.1-mini": p(0.4, 1.6, 0.1, 0),
	"gpt-4.1-nano": p(0.1, 0.4, 0.025, 0),
	"gpt-4o":       p(2.5, 10, 1.25, 0),
	"gpt-4o-mini":  p(0.15, 0.6, 0.075, 0),
	"o3":           p(2, 8, 0.5, 0),
	"o4-mini":      p(1.1, 4.4, 0.275, 0),
	// Google (≤200k-token prompt tier)
	"gemini-2.5-pro":        p(1.25, 10, 0.31, -1),
	"gemini-2.5-flash":      p(0.3, 2.5, 0.075, -1),
	"gemini-2.5-flash-lite": p(0.1, 0.4, 0.025, -1),
	// Others
	"minimax-m2": p(0.3, 1.2, -1, -1),
	"glm-4.6":    p(0.6, 2.2, -1, -1),
}

// Usage is the token breakdown of one request. Input includes cache reads
// and writes (as in ir.Usage).
type Usage struct {
	Input, Output, CacheRead, CacheWrite int64
}

// Table resolves prices with user overrides taking precedence.
type Table struct {
	overrides map[string]config.Price
}

func New(overrides map[string]config.Price) *Table {
	o := make(map[string]config.Price, len(overrides))
	for k, v := range overrides {
		o[strings.ToLower(k)] = v
	}
	return &Table{overrides: o}
}

var dateSuffix = regexp.MustCompile(`(-\d{8}|-\d{4}-\d{2}-\d{2}|@\d{8}|-latest)$`)

// normalize strips vendor prefixes ("z-ai/glm-4.6") and date/version tags
// ("claude-sonnet-4-5-20250929", "...@20250929", "-latest").
func normalize(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	return dateSuffix.ReplaceAllString(m, "")
}

// prefixMatch returns the longest key that m starts with at a "-"/"."
// boundary, e.g. "gpt-5-mini-2025" → "gpt-5-mini".
func prefixMatch(tab map[string]config.Price, m string) (config.Price, bool) {
	best := ""
	for k := range tab {
		if len(k) > len(best) && strings.HasPrefix(m, k) && (len(m) == len(k) || m[len(k)] == '-' || m[len(k)] == '.') {
			best = k
		}
	}
	if best == "" {
		return config.Price{}, false
	}
	return tab[best], true
}

// Lookup finds the price for a request served by provider with upstream
// model upModel, requested as reqModel.
func (t *Table) Lookup(provider, upModel, reqModel string) (config.Price, bool) {
	prov := strings.ToLower(provider)
	if t != nil && len(t.overrides) > 0 {
		for _, k := range []string{prov + "/" + strings.ToLower(upModel), strings.ToLower(upModel), strings.ToLower(reqModel), prov + "/" + normalize(upModel), normalize(upModel), normalize(reqModel)} {
			if pr, ok := t.overrides[k]; ok && k != "" && k != prov+"/" {
				return pr, true
			}
		}
		if pr, ok := t.overrides[prov+"/*"]; ok && prov != "" {
			return pr, true
		}
		for _, m := range []string{normalize(upModel), normalize(reqModel)} {
			if pr, ok := prefixMatch(t.overrides, m); ok && m != "" {
				return pr, true
			}
		}
	}
	for _, m := range []string{normalize(upModel), normalize(reqModel)} {
		if m == "" {
			continue
		}
		if pr, ok := Builtin[m]; ok {
			return pr, true
		}
		if pr, ok := prefixMatch(Builtin, m); ok {
			return pr, true
		}
	}
	return config.Price{}, false
}

// Cost returns the estimated USD cost, or 0 when the model has no price.
func (t *Table) Cost(provider, upModel, reqModel string, u Usage) float64 {
	if u.Input == 0 && u.Output == 0 {
		return 0
	}
	pr, ok := t.Lookup(provider, upModel, reqModel)
	if !ok {
		return 0
	}
	return Of(pr, u)
}

// Of applies a price to usage.
func Of(pr config.Price, u Usage) float64 {
	cr, cw := pr.Input, pr.Input
	if pr.CacheRead != nil {
		cr = *pr.CacheRead
	}
	if pr.CacheWrite != nil {
		cw = *pr.CacheWrite
	}
	plain := max(u.Input-u.CacheRead-u.CacheWrite, 0)
	return (float64(plain)*pr.Input + float64(u.CacheRead)*cr + float64(u.CacheWrite)*cw + float64(u.Output)*pr.Output) / 1e6
}
