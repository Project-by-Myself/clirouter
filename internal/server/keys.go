package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/usage"
)

// Key management API: create, update, rotate and delete one client key
// without round-tripping the whole config (LiteLLM-style /key/* endpoints).

// keyView is a client key as returned by the API: never the stored key,
// only its last characters, plus live status and usage.
type keyView struct {
	config.ClientKey
	Hint   string         `json:"key_hint"`
	Status string         `json:"status"`
	Usage  usage.Snapshot `json:"usage"`
	// Key is the plaintext key, only present right after create/rotate.
	Plain string `json:"key,omitempty"`
}

func keyHint(stored string) string {
	switch {
	case strings.HasPrefix(stored, "env:"):
		return stored
	case strings.HasPrefix(stored, config.HashPrefix):
		return "…" + stored[len(stored)-4:]
	case len(stored) >= 16:
		return "…" + stored[len(stored)-4:]
	}
	return "****"
}

func (s *Server) view(k *config.ClientKey) keyView {
	v := keyView{ClientKey: *k, Hint: keyHint(k.Key), Status: s.keyStatus(k), Usage: s.h.Usage.Snapshot(usage.Key, k.Name)}
	v.ClientKey.Key = ""
	return v
}

// NewClientKey returns a random client key.
func NewClientKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-clr-" + base64.RawURLEncoding.EncodeToString(b), nil
}

// mutate applies fn to a deep copy of the live config and commits it.
func (s *Server) mutate(fn func(c *config.Config) (int, error)) (*config.Config, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(s.h.Runtime().Cfg)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	next := config.Default()
	if err := json.Unmarshal(b, next); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	// admin_password is omitted from JSON when empty only; keep it exactly.
	next.AdminPassword = s.h.Runtime().Cfg.AdminPassword
	if status, err := fn(next); err != nil {
		return nil, status, err
	}
	if status, err := s.commit(next); err != nil {
		return nil, status, err
	}
	return next, 0, nil
}

func findKey(c *config.Config, name string) int {
	for i := range c.APIKeys {
		if c.APIKeys[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *Server) adminListKeys(w http.ResponseWriter, r *http.Request) {
	cfg := s.h.Runtime().Cfg
	out := make([]keyView, 0, len(cfg.APIKeys))
	for i := range cfg.APIKeys {
		out = append(out, s.view(&cfg.APIKeys[i]))
	}
	jsonOK(w, out)
}

func (s *Server) adminGetKey(w http.ResponseWriter, r *http.Request) {
	cfg := s.h.Runtime().Cfg
	i := findKey(cfg, r.PathValue("name"))
	if i < 0 {
		jsonErr(w, http.StatusNotFound, "no such key")
		return
	}
	jsonOK(w, s.view(&cfg.APIKeys[i]))
}

// keyPatch lists the fields a client may set; nil means "unchanged".
type keyPatch struct {
	Name         *string   `json:"name"`
	Note         *string   `json:"note"`
	Models       *[]string `json:"models"`
	BudgetUSD    *float64  `json:"budget_usd"`
	BudgetTokens *int64    `json:"budget_tokens"`
	BudgetPeriod *string   `json:"budget_period"`
	RPM          *int      `json:"rpm"`
	TPM          *int64    `json:"tpm"`
	MaxParallel  *int      `json:"max_parallel"`
	ExpiresAt    *string   `json:"expires_at"`
	// ExpiresIn sets expires_at relative to now, e.g. "720h" or "30d".
	ExpiresIn *string `json:"expires_in"`
	Disabled  *bool   `json:"disabled"`
	// Key lets the caller choose the plaintext key (create only).
	Key *string `json:"key"`
}

func parseExpiresIn(v string) (time.Time, error) {
	if d, ok := strings.CutSuffix(v, "d"); ok {
		var n int
		if _, err := fmt.Sscanf(d, "%d", &n); err == nil && n > 0 {
			return time.Now().AddDate(0, 0, n), nil
		}
	}
	dur, err := time.ParseDuration(v)
	if err != nil || dur <= 0 {
		return time.Time{}, fmt.Errorf("expires_in %q: want a duration like 720h or 30d", v)
	}
	return time.Now().Add(dur), nil
}

func (p *keyPatch) apply(k *config.ClientKey) error {
	if p.Name != nil {
		k.Name = strings.TrimSpace(*p.Name)
	}
	if p.Note != nil {
		k.Note = *p.Note
	}
	if p.Models != nil {
		k.Models = *p.Models
	}
	if p.BudgetUSD != nil {
		k.BudgetUSD = *p.BudgetUSD
	}
	if p.BudgetTokens != nil {
		k.BudgetTokens = *p.BudgetTokens
	}
	if p.BudgetPeriod != nil {
		k.BudgetPeriod = *p.BudgetPeriod
	}
	if p.RPM != nil {
		k.RPM = *p.RPM
	}
	if p.TPM != nil {
		k.TPM = *p.TPM
	}
	if p.MaxParallel != nil {
		k.MaxParallel = *p.MaxParallel
	}
	if p.ExpiresAt != nil {
		k.ExpiresAt = *p.ExpiresAt
	}
	if p.ExpiresIn != nil {
		t, err := parseExpiresIn(*p.ExpiresIn)
		if err != nil {
			return err
		}
		k.ExpiresAt = t.Format(time.RFC3339)
	}
	if p.Disabled != nil {
		k.Disabled = *p.Disabled
	}
	return nil
}

func decodePatch(r *http.Request) (*keyPatch, error) {
	var p keyPatch
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &p, nil
}

func (s *Server) adminCreateKey(w http.ResponseWriter, r *http.Request) {
	p, err := decodePatch(r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Name == nil || strings.TrimSpace(*p.Name) == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	plain := ""
	if p.Key != nil && *p.Key != "" {
		if len(*p.Key) < 16 {
			jsonErr(w, http.StatusBadRequest, "key must be at least 16 characters")
			return
		}
		plain = *p.Key
	} else if plain, err = NewClientKey(); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var created config.ClientKey
	cfg, status, err := s.mutate(func(c *config.Config) (int, error) {
		if findKey(c, strings.TrimSpace(*p.Name)) >= 0 {
			return http.StatusConflict, fmt.Errorf("a key named %q already exists", strings.TrimSpace(*p.Name))
		}
		k := config.ClientKey{Key: config.HashClientKey(plain), CreatedAt: time.Now().Format(time.RFC3339)}
		if err := p.apply(&k); err != nil {
			return http.StatusBadRequest, err
		}
		c.APIKeys = append(c.APIKeys, k)
		created = k
		return 0, nil
	})
	if err != nil {
		jsonErr(w, status, err.Error())
		return
	}
	v := s.view(&cfg.APIKeys[findKey(cfg, created.Name)])
	v.Plain = plain
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, v)
}

func (s *Server) adminUpdateKey(w http.ResponseWriter, r *http.Request) {
	p, err := decodePatch(r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Key != nil {
		jsonErr(w, http.StatusBadRequest, "use POST /admin/api/keys/{name}/rotate to change the key")
		return
	}
	name := r.PathValue("name")
	newName := name
	cfg, status, err := s.mutate(func(c *config.Config) (int, error) {
		i := findKey(c, name)
		if i < 0 {
			return http.StatusNotFound, errors.New("no such key")
		}
		if err := p.apply(&c.APIKeys[i]); err != nil {
			return http.StatusBadRequest, err
		}
		newName = c.APIKeys[i].Name
		return 0, nil
	})
	if err != nil {
		jsonErr(w, status, err.Error())
		return
	}
	jsonOK(w, s.view(&cfg.APIKeys[findKey(cfg, newName)]))
}

func (s *Server) adminRotateKey(w http.ResponseWriter, r *http.Request) {
	plain, err := NewClientKey()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := r.PathValue("name")
	cfg, status, err := s.mutate(func(c *config.Config) (int, error) {
		i := findKey(c, name)
		if i < 0 {
			return http.StatusNotFound, errors.New("no such key")
		}
		c.APIKeys[i].Key = config.HashClientKey(plain)
		return 0, nil
	})
	if err != nil {
		jsonErr(w, status, err.Error())
		return
	}
	v := s.view(&cfg.APIKeys[findKey(cfg, name)])
	v.Plain = plain
	jsonOK(w, v)
}

func (s *Server) adminDeleteKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, status, err := s.mutate(func(c *config.Config) (int, error) {
		i := findKey(c, name)
		if i < 0 {
			return http.StatusNotFound, errors.New("no such key")
		}
		c.APIKeys = append(c.APIKeys[:i], c.APIKeys[i+1:]...)
		return 0, nil
	})
	if err != nil {
		jsonErr(w, status, err.Error())
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}
