// Package config loads and validates the YAML configuration. The file is the
// single source of truth; the dashboard edits it through Save (atomic write).
package config

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }
func (d Duration) D() time.Duration          { return time.Duration(d) }

type Config struct {
	Listen        string            `yaml:"listen" json:"listen"`
	APIKeys       []ClientKey       `yaml:"api_keys,omitempty" json:"api_keys"`
	AdminPassword string            `yaml:"admin_password,omitempty" json:"admin_password,omitempty"`
	DataDir       string            `yaml:"data_dir,omitempty" json:"data_dir"`
	Timeouts      Timeouts          `yaml:"timeouts" json:"timeouts"`
	Routing       Routing           `yaml:"routing" json:"routing"`
	Logging       Logging           `yaml:"logging" json:"logging"`
	Providers     []Provider        `yaml:"providers" json:"providers"`
	Combos        []Combo           `yaml:"combos,omitempty" json:"combos"`
	Aliases       map[string]string `yaml:"aliases,omitempty" json:"aliases"`
	// PublicURL is how clients on other machines reach this server (used in
	// share messages and the dashboard's setup snippets). Optional.
	PublicURL string `yaml:"public_url,omitempty" json:"public_url,omitempty"`
	// Pricing overrides the built-in list prices used for cost estimates.
	// Keys are "model", "provider/model" or "provider/*".
	Pricing map[string]Price `yaml:"pricing,omitempty" json:"pricing,omitempty"`
	Alerts  Alerts           `yaml:"alerts,omitempty" json:"alerts"`
	// PricingSync refreshes prices from a public list (LiteLLM's by default).
	PricingSync PricingSync `yaml:"pricing_sync,omitempty" json:"pricing_sync"`
}

type PricingSync struct {
	Auto bool   `yaml:"auto,omitempty" json:"auto,omitempty"` // refresh daily
	URL  string `yaml:"url,omitempty" json:"url,omitempty"`   // LiteLLM-format JSON; empty = LiteLLM's
}

// Alerts sends notifications when budgets cross thresholds, keys are about
// to expire or upstream quota runs low. Each channel is optional.
type Alerts struct {
	WebhookURL       string `yaml:"webhook_url,omitempty" json:"webhook_url,omitempty"` // POSTed a JSON event
	TelegramBotToken string `yaml:"telegram_bot_token,omitempty" json:"telegram_bot_token,omitempty"`
	TelegramChatID   string `yaml:"telegram_chat_id,omitempty" json:"telegram_chat_id,omitempty"`
	// Thresholds are percentages of a budget that trigger an alert (default 80, 100).
	Thresholds []int `yaml:"thresholds,omitempty" json:"thresholds,omitempty"`
	// ExpiryDays warns this many days before a key expires (default 3; -1 = off).
	ExpiryDays int `yaml:"expiry_days,omitempty" json:"expiry_days,omitempty"`
	// QuotaPct alerts when an upstream quota window reaches this % used (default 90; -1 = off).
	QuotaPct int `yaml:"quota_pct,omitempty" json:"quota_pct,omitempty"`
}

// Enabled reports whether any alert channel is configured.
func (a *Alerts) Enabled() bool {
	return a.WebhookURL != "" || (a.TelegramBotToken != "" && a.TelegramChatID != "")
}

// Levels returns the thresholds (default 80, 100), ascending.
func (a *Alerts) Levels() []int {
	if len(a.Thresholds) == 0 {
		return []int{80, 100}
	}
	out := append([]int(nil), a.Thresholds...)
	sort.Ints(out)
	return out
}

// Price is in USD per 1M tokens. Unset cache prices fall back to Input.
type Price struct {
	Input      float64  `yaml:"input" json:"input"`
	Output     float64  `yaml:"output" json:"output"`
	CacheRead  *float64 `yaml:"cache_read,omitempty" json:"cache_read,omitempty"`
	CacheWrite *float64 `yaml:"cache_write,omitempty" json:"cache_write,omitempty"`
}

// Budget periods. An empty period means monthly.
const (
	PeriodDaily   = "daily"
	PeriodWeekly  = "weekly"
	PeriodMonthly = "monthly"
	PeriodTotal   = "total"
)

// NormPeriod returns p, or monthly when p is empty.
func NormPeriod(p string) string {
	if p == "" {
		return PeriodMonthly
	}
	return p
}

func validPeriod(p string) bool {
	switch p {
	case "", PeriodDaily, PeriodWeekly, PeriodMonthly, PeriodTotal:
		return true
	}
	return false
}

type ClientKey struct {
	Name string `yaml:"name" json:"name"`
	Key  string `yaml:"key" json:"key"`
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
	// Models limits which model names (as requested by the client) the key
	// may call. A trailing "*" matches a prefix. Empty allows every model.
	Models []string `yaml:"models,omitempty" json:"models,omitempty"`
	// Spending limits per BudgetPeriod; 0 = unlimited. Tokens count input + output.
	BudgetUSD    float64 `yaml:"budget_usd,omitempty" json:"budget_usd,omitempty"`
	BudgetTokens int64   `yaml:"budget_tokens,omitempty" json:"budget_tokens,omitempty"`
	BudgetPeriod string  `yaml:"budget_period,omitempty" json:"budget_period,omitempty"`
	RPM          int     `yaml:"rpm,omitempty" json:"rpm,omitempty"`                   // requests per minute
	TPM          int64   `yaml:"tpm,omitempty" json:"tpm,omitempty"`                   // tokens (input+output) per minute
	MaxParallel  int     `yaml:"max_parallel,omitempty" json:"max_parallel,omitempty"` // concurrent requests
	ExpiresAt    string  `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`     // RFC3339
	Disabled     bool    `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	CreatedAt    string  `yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

// Expiry parses ExpiresAt; the zero time means the key never expires.
func (k *ClientKey) Expiry() time.Time {
	t, _ := time.Parse(time.RFC3339, k.ExpiresAt)
	return t
}

// AllowsModel reports whether the key may call model.
func (k *ClientKey) AllowsModel(model string) bool {
	if len(k.Models) == 0 {
		return true
	}
	m := strings.ToLower(model)
	for _, p := range k.Models {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "*" || p == m {
			return true
		}
		if pre, ok := strings.CutSuffix(p, "*"); ok && strings.HasPrefix(m, pre) {
			return true
		}
	}
	return false
}

type Timeouts struct {
	Connect   Duration `yaml:"connect" json:"connect"`       // TCP + TLS to upstream
	FirstByte Duration `yaml:"first_byte" json:"first_byte"` // request sent -> response headers
	Idle      Duration `yaml:"idle" json:"idle"`             // max silence between upstream bytes
	Total     Duration `yaml:"total" json:"total"`           // hard cap per request
	EarlyPing Duration `yaml:"early_ping" json:"early_ping"` // commit stream headers + ping after this wait
	Keepalive Duration `yaml:"keepalive" json:"keepalive"`   // ping interval while the client sees no bytes
}

type Routing struct {
	Strategy        string `yaml:"strategy" json:"strategy"` // round-robin | fill-first | least-busy
	MaxAttempts     int    `yaml:"max_attempts" json:"max_attempts"`
	SessionAffinity bool   `yaml:"session_affinity" json:"session_affinity"`
	MaxBodyMB       int    `yaml:"max_body_mb" json:"max_body_mb"`
}

type Logging struct {
	Level         string `yaml:"level" json:"level"`
	RetentionDays int    `yaml:"retention_days" json:"retention_days"`
	CaptureBodies bool   `yaml:"capture_bodies" json:"capture_bodies"` // store request/response bodies of failed requests
}

type Provider struct {
	ID       string            `yaml:"id" json:"id"`
	Type     string            `yaml:"type" json:"type"`
	BaseURL  string            `yaml:"base_url,omitempty" json:"base_url"`
	Keys     []string          `yaml:"keys,omitempty" json:"keys"`
	OAuth    []string          `yaml:"oauth,omitempty" json:"oauth"` // account ids stored in data_dir/auth
	Models   []string          `yaml:"models,omitempty" json:"models"`
	ModelMap map[string]string `yaml:"model_map,omitempty" json:"model_map"`
	Headers  map[string]string `yaml:"headers,omitempty" json:"headers"`
	Proxy    string            `yaml:"proxy,omitempty" json:"proxy"`
	Priority int               `yaml:"priority,omitempty" json:"priority"`
	Disabled bool              `yaml:"disabled,omitempty" json:"disabled"`
	// Per-provider overrides; zero means use the global timeout.
	FirstByte Duration `yaml:"first_byte,omitempty" json:"first_byte"`
	Idle      Duration `yaml:"idle,omitempty" json:"idle"`
	// Estimated spend cap per BudgetPeriod; once reached the router skips
	// this provider and falls back to the next target. 0 = unlimited.
	BudgetUSD    float64 `yaml:"budget_usd,omitempty" json:"budget_usd,omitempty"`
	BudgetPeriod string  `yaml:"budget_period,omitempty" json:"budget_period,omitempty"`
}

type Combo struct {
	Name     string   `yaml:"name" json:"name"`
	Strategy string   `yaml:"strategy" json:"strategy"` // fallback | round-robin
	Targets  []string `yaml:"targets" json:"targets"`
}

// Provider types and their default endpoints.
var DefaultBaseURL = map[string]string{
	"anthropic":         "https://api.anthropic.com",
	"openai":            "https://api.openai.com/v1",
	"openai-compatible": "",
	"gemini":            "https://generativelanguage.googleapis.com",
	"vertex":            "https://aiplatform.googleapis.com",
	"codex":             "https://chatgpt.com/backend-api/codex",
	"claude-oauth":      "https://api.anthropic.com",
	"copilot":           "https://api.githubcopilot.com",
	"gemini-cli":        "https://cloudcode-pa.googleapis.com",
}

func Default() *Config {
	return &Config{
		Listen: "127.0.0.1:8317",
		Timeouts: Timeouts{
			Connect:   Duration(10 * time.Second),
			FirstByte: Duration(90 * time.Second),
			Idle:      Duration(180 * time.Second),
			Total:     Duration(60 * time.Minute),
			EarlyPing: Duration(2 * time.Second),
			Keepalive: Duration(15 * time.Second),
		},
		Routing: Routing{Strategy: "round-robin", MaxAttempts: 6, SessionAffinity: true, MaxBodyMB: 64},
		Logging: Logging{Level: "info", RetentionDays: 14},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(filepath.Dir(path), "data")
	}
	return cfg, cfg.Validate()
}

func (c *Config) Validate() error {
	var errs []error
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen %q: want host:port (%v)", c.Listen, err))
	}
	// A zero timeout would fail every request at once (an emptied field in
	// the dashboard is sent as 0), so zero means "use the default".
	def := Default()
	for _, d := range []struct {
		name   string
		v      *Duration
		dflt   Duration
		zeroOK bool // 0 disables the feature
	}{
		{"connect", &c.Timeouts.Connect, def.Timeouts.Connect, false},
		{"first_byte", &c.Timeouts.FirstByte, def.Timeouts.FirstByte, false},
		{"idle", &c.Timeouts.Idle, def.Timeouts.Idle, false},
		{"total", &c.Timeouts.Total, def.Timeouts.Total, false},
		{"early_ping", &c.Timeouts.EarlyPing, 0, true},
		{"keepalive", &c.Timeouts.Keepalive, 0, true},
	} {
		switch {
		case *d.v < 0:
			errs = append(errs, fmt.Errorf("timeouts.%s must not be negative", d.name))
		case *d.v == 0 && !d.zeroOK:
			*d.v = d.dflt
		}
	}
	if c.Logging.RetentionDays <= 0 {
		c.Logging.RetentionDays = def.Logging.RetentionDays
	}
	seen := map[string]bool{}
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ID == "" {
			errs = append(errs, fmt.Errorf("providers[%d]: id is required", i))
			continue
		}
		if strings.Contains(p.ID, "/") {
			errs = append(errs, fmt.Errorf("provider %q: id must not contain '/'", p.ID))
		}
		if seen[p.ID] {
			errs = append(errs, fmt.Errorf("provider %q: duplicate id", p.ID))
		}
		seen[p.ID] = true
		def, ok := DefaultBaseURL[p.Type]
		if !ok {
			errs = append(errs, fmt.Errorf("provider %q: unknown type %q", p.ID, p.Type))
		}
		if p.BaseURL == "" {
			p.BaseURL = def
		}
		p.BaseURL = strings.TrimRight(p.BaseURL, "/")
		if p.BaseURL == "" {
			errs = append(errs, fmt.Errorf("provider %q: base_url is required for type %s", p.ID, p.Type))
		}
		if p.FirstByte < 0 || p.Idle < 0 {
			errs = append(errs, fmt.Errorf("provider %q: timeouts must not be negative", p.ID))
		}
		if p.BudgetUSD < 0 || !validPeriod(p.BudgetPeriod) {
			errs = append(errs, fmt.Errorf("provider %q: budget_usd must be >= 0 and budget_period one of daily, weekly, monthly, total", p.ID))
		}
	}
	names := map[string]bool{}
	for i, k := range c.APIKeys {
		if k.Name == "" {
			errs = append(errs, fmt.Errorf("api_keys[%d]: name is required", i))
			continue
		}
		if names[k.Name] {
			errs = append(errs, fmt.Errorf("api key %q: duplicate name", k.Name))
		}
		names[k.Name] = true
		if k.BudgetUSD < 0 || k.BudgetTokens < 0 || k.RPM < 0 || k.TPM < 0 || k.MaxParallel < 0 {
			errs = append(errs, fmt.Errorf("api key %q: limits must not be negative", k.Name))
		}
		if !validPeriod(k.BudgetPeriod) {
			errs = append(errs, fmt.Errorf("api key %q: budget_period must be daily, weekly, monthly or total", k.Name))
		}
		if k.ExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339, k.ExpiresAt); err != nil {
				errs = append(errs, fmt.Errorf("api key %q: expires_at must be RFC3339 (e.g. 2026-12-31T23:59:59+07:00)", k.Name))
			}
		}
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("public_url %q: want http(s)://host[:port]", c.PublicURL))
		}
		c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	}
	if u := c.Alerts.WebhookURL; u != "" && !strings.HasPrefix(u, "env:") {
		if pu, err := url.Parse(u); err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			errs = append(errs, fmt.Errorf("alerts.webhook_url: want an http(s) URL"))
		}
	}
	if (c.Alerts.TelegramBotToken == "") != (c.Alerts.TelegramChatID == "") {
		errs = append(errs, fmt.Errorf("alerts: telegram_bot_token and telegram_chat_id must be set together"))
	}
	for _, t := range c.Alerts.Thresholds {
		if t <= 0 || t > 1000 {
			errs = append(errs, fmt.Errorf("alerts.thresholds: %d is not a percentage between 1 and 1000", t))
		}
	}
	for m, p := range c.Pricing {
		if p.Input < 0 || p.Output < 0 || (p.CacheRead != nil && *p.CacheRead < 0) || (p.CacheWrite != nil && *p.CacheWrite < 0) {
			errs = append(errs, fmt.Errorf("pricing %q: prices must not be negative", m))
		}
	}
	for _, cb := range c.Combos {
		if cb.Name == "" || len(cb.Targets) == 0 {
			errs = append(errs, fmt.Errorf("combo %q: name and targets are required", cb.Name))
		}
		if seen[cb.Name] {
			errs = append(errs, fmt.Errorf("combo %q: name collides with a provider id", cb.Name))
		}
	}
	return errors.Join(errs...)
}

// Secret resolves "env:NAME" references.
func Secret(s string) string {
	if name, ok := strings.CutPrefix(s, "env:"); ok {
		return os.Getenv(name)
	}
	return s
}

// HashPrefix marks a client key stored as a SHA-256 digest instead of the
// key itself. Client keys are long random strings, so an unsalted digest is
// enough: the config file no longer holds anything a client could send.
const HashPrefix = "sha256:"

// HashClientKey returns the stored form of a client key.
func HashClientKey(k string) string {
	sum := sha256.Sum256([]byte(k))
	return HashPrefix + hex.EncodeToString(sum[:])
}

// SecretEqual compares two secrets in constant time, independent of their
// lengths (both sides are hashed to fixed-size digests first).
func SecretEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1 && a != "" && b != ""
}

// ClientKeyMatches reports whether the presented key cand matches a
// configured key (plain, "env:NAME" or "sha256:<hex>").
func ClientKeyMatches(cand, stored string) bool {
	if cand == "" {
		return false
	}
	if h, ok := strings.CutPrefix(stored, HashPrefix); ok {
		want, err := hex.DecodeString(h)
		if err != nil || len(want) != sha256.Size {
			return false
		}
		got := sha256.Sum256([]byte(cand))
		return subtle.ConstantTimeCompare(got[:], want) == 1
	}
	return SecretEqual(cand, Secret(stored))
}

// Save writes atomically (temp file + rename) with 0600 permissions.
func Save(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return writeFile(path, b)
}

// SetTopLevel sets one top-level scalar key in the YAML file, keeping the
// rest of the document (including comments) as written.
func SetTopLevel(path, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", path)
	}
	m := doc.Content[0]
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	found := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			found = true
		}
	}
	if !found {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
	}
	b, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return writeFile(path, b)
}

func writeFile(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
