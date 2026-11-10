// Package config loads and validates the YAML configuration. The file is the
// single source of truth; the dashboard edits it through Save (atomic write).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
func (d Duration) D() time.Duration         { return time.Duration(d) }

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
}

type ClientKey struct {
	Name string `yaml:"name" json:"name"`
	Key  string `yaml:"key" json:"key"`
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

// Save writes atomically (temp file + rename) with 0600 permissions.
func Save(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
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
