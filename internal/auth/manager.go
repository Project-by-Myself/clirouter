package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/router"
)

// Manager owns the credential store, per-account token sources, background
// refresh and in-progress login flows.
type Manager struct {
	Logger *slog.Logger

	store *Store
	hc    *http.Client

	mu      sync.Mutex
	sources map[string]*source
	flows   map[string]*flow

	// Tunables (overridden in tests).
	now        func() time.Time
	syncWindow time.Duration // Token() refreshes synchronously inside this window
	bgWindow   time.Duration // background loop refreshes inside this window
	bgJitter   time.Duration // per-account random reduction of bgWindow
	coalesce   time.Duration // Refresh() reuses a result younger than this
	backoffMin time.Duration
	backoffMax time.Duration
	tick       time.Duration
	flowTTL    time.Duration
	pollUnit   time.Duration // device-flow interval unit
	refreshTO  time.Duration
}

func NewManager(dataDir string, httpClient *http.Client) *Manager {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Manager{
		Logger:     slog.Default(),
		store:      NewStore(dataDir),
		hc:         httpClient,
		sources:    map[string]*source{},
		flows:      map[string]*flow{},
		now:        time.Now,
		syncWindow: 2 * time.Minute,
		bgWindow:   15 * time.Minute,
		bgJitter:   3 * time.Minute,
		coalesce:   5 * time.Second,
		backoffMin: time.Minute,
		backoffMax: 30 * time.Minute,
		tick:       30 * time.Second,
		flowTTL:    15 * time.Minute,
		pollUnit:   time.Second,
		refreshTO:  45 * time.Second,
	}
}

// Store exposes the underlying credential store.
func (m *Manager) Store() *Store { return m.store }

// TokenSource returns the source for accountID. It never returns nil; when no
// credential exists Token() fails with "account <id> not logged in".
// providerID is informational: credentials are keyed by account id only.
func (m *Manager) TokenSource(providerID, accountID string) router.TokenSource {
	return m.source(accountID)
}

func (m *Manager) source(id string) *source {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sources[id]
	if s == nil {
		s = &source{m: m, id: id, jitter: time.Duration(rand.Int64N(int64(m.bgJitter) + 1))}
		m.sources[id] = s
	}
	return s
}

// AccountInfo is the dashboard view of one stored account (no secrets).
type AccountInfo struct {
	AccountID   string     `json:"account_id"`
	Type        string     `json:"type"`
	Email       string     `json:"email,omitempty"`
	Plan        string     `json:"plan,omitempty"`
	ProjectID   string     `json:"project_id,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastRefresh *time.Time `json:"last_refresh,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

// List returns all stored accounts.
func (m *Manager) List() ([]AccountInfo, error) {
	ids, err := m.store.IDs()
	if err != nil {
		return nil, err
	}
	out := make([]AccountInfo, 0, len(ids))
	for _, id := range ids {
		s := m.source(id)
		s.mu.Lock()
		s.ensureLoaded()
		info := AccountInfo{AccountID: id, LastError: s.lastErr}
		if c := s.cred; c != nil {
			info.Type, info.Email, info.Plan, info.ProjectID = c.Type, c.Email, c.Plan, c.ProjectID
			if !c.ExpiresAt.IsZero() {
				t := c.ExpiresAt
				info.ExpiresAt = &t
			}
			if !c.LastRefresh.IsZero() {
				t := c.LastRefresh
				info.LastRefresh = &t
			}
		}
		s.mu.Unlock()
		out = append(out, info)
	}
	return out, nil
}

// Delete removes an account's credential. Existing sources start failing with
// "not logged in".
func (m *Manager) Delete(id string) error {
	s := m.source(id)
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	err := m.store.Delete(id)
	s.mu.Lock()
	s.cred, s.loaded, s.loadedAt, s.lastErr = nil, true, m.now(), ""
	s.gen++
	s.mu.Unlock()
	return err
}

// put installs a freshly logged-in credential (persisted first).
func (m *Manager) put(id string, c *Credential) error {
	s := m.source(id)
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if err := m.store.Save(id, c); err != nil {
		return err
	}
	s.mu.Lock()
	s.cred, s.loaded, s.lastErr, s.fails, s.nextTry = c.clone(), true, "", 0, time.Time{}
	s.gen++
	s.mu.Unlock()
	return nil
}

// Start runs the background refresh loop and flow janitor until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(m.tick)
		defer t.Stop()
		m.backgroundPass(ctx)
		for {
			select {
			case <-ctx.Done():
				m.closeFlows()
				return
			case <-t.C:
				m.backgroundPass(ctx)
			}
		}
	}()
}

func (m *Manager) backgroundPass(ctx context.Context) {
	m.expireFlows()
	ids, err := m.store.IDs()
	if err != nil {
		m.Logger.Warn("oauth: list accounts", "err", err)
		return
	}
	now := m.now()
	for _, id := range ids {
		s := m.source(id)
		if s.backgroundDue(now) {
			go func() {
				if ctx.Err() != nil {
					return
				}
				if err := s.refresh(false, true); err != nil {
					m.Logger.Warn("oauth: background refresh failed", "account", id, "err", err)
				}
			}()
		}
	}
}

// source implements router.TokenSource for one account.
type source struct {
	m      *Manager
	id     string
	jitter time.Duration

	persistMu sync.Mutex

	mu       sync.Mutex
	cred     *Credential
	loaded   bool
	loadedAt time.Time
	gen      uint64

	inflight  chan struct{}
	lastRes   error
	lastResAt time.Time

	lastErr string
	fails   int
	nextTry time.Time
}

// ensureLoaded reads the credential from disk once (retrying a missing file
// every 10s so logins done by another process are picked up). Caller holds mu.
func (s *source) ensureLoaded() {
	now := s.m.now()
	if s.loaded && (s.cred != nil || now.Sub(s.loadedAt) < 10*time.Second) {
		return
	}
	c, err := s.m.store.Load(s.id)
	s.loaded, s.loadedAt = true, now
	switch {
	case err == nil:
		s.cred = c
	case errors.Is(err, ErrNotFound):
		s.cred = nil
	default:
		s.cred = nil
		s.lastErr = err.Error()
	}
}

func (s *source) notLoggedIn() error { return fmt.Errorf("account %s not logged in", s.id) }

// freshFor reports whether the current access token is valid for at least d. Caller holds mu.
func (s *source) freshFor(d time.Duration) bool {
	c := s.cred
	if c == nil || c.AccessToken == "" {
		return false
	}
	return c.ExpiresAt.IsZero() || c.ExpiresAt.Sub(s.m.now()) > d
}

func (s *source) Token() (string, error) {
	s.mu.Lock()
	s.ensureLoaded()
	if s.cred == nil {
		s.mu.Unlock()
		return "", s.notLoggedIn()
	}
	if s.freshFor(s.m.syncWindow) {
		t := s.cred.AccessToken
		s.mu.Unlock()
		return t, nil
	}
	s.mu.Unlock()

	err := s.refresh(false, false)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred == nil {
		return "", s.notLoggedIn()
	}
	// A failed refresh still leaves a usable token until it actually expires.
	if s.freshFor(0) {
		return s.cred.AccessToken, nil
	}
	if err == nil {
		err = errors.New("refresh returned no usable token")
	}
	return "", fmt.Errorf("account %s: token refresh failed: %w", s.id, err)
}

func (s *source) Refresh() error { return s.refresh(true, false) }

// refresh performs a single-flight refresh. force=false skips it when the
// token is already fresh; force=true reuses a result younger than coalesce.
func (s *source) refresh(force, background bool) error {
	s.mu.Lock()
	s.ensureLoaded()
	if ch := s.inflight; ch != nil {
		s.mu.Unlock()
		<-ch
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.lastRes
	}
	if s.cred == nil {
		s.mu.Unlock()
		return s.notLoggedIn()
	}
	now := s.m.now()
	if force && !s.lastResAt.IsZero() && now.Sub(s.lastResAt) < s.m.coalesce {
		defer s.mu.Unlock()
		return s.lastRes
	}
	if !force {
		win := s.m.syncWindow
		if background {
			win = s.m.bgWindow - s.jitter
		}
		if s.freshFor(win) {
			s.mu.Unlock()
			return nil
		}
	}
	ch := make(chan struct{})
	s.inflight = ch
	cur, gen := s.cred.clone(), s.gen
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.m.refreshTO)
	next, err := s.m.refreshCredential(ctx, cur)
	cancel()
	var saveErr error
	stale := false
	if err == nil {
		next.LastRefresh = s.m.now()
		// persistMu orders this write against Delete/put so a refresh racing a
		// logout or re-login cannot resurrect or clobber the stored credential.
		s.persistMu.Lock()
		defer s.persistMu.Unlock()
		s.mu.Lock()
		stale = s.gen != gen
		s.mu.Unlock()
		if !stale {
			// Rotated refresh tokens are single-use: persist before anyone uses the new pair.
			if saveErr = s.m.store.Save(s.id, next); saveErr != nil {
				s.m.Logger.Error("oauth: persist refreshed credential failed", "account", s.id, "err", saveErr)
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now = s.m.now()
	if err == nil && !stale && s.gen == gen {
		s.cred = next
	}
	switch {
	case err != nil:
		s.fails++
		s.lastErr = err.Error()
		s.nextTry = now.Add(s.backoff())
	case saveErr != nil:
		s.fails = 0
		s.lastErr = "persist: " + saveErr.Error()
		s.nextTry = time.Time{}
	default:
		s.fails, s.lastErr, s.nextTry = 0, "", time.Time{}
	}
	s.lastRes, s.lastResAt = err, now
	s.inflight = nil
	close(ch)
	return err
}

// backoff returns min(backoffMin*2^(fails-1), backoffMax) with ±10% jitter. Caller holds mu.
func (s *source) backoff() time.Duration {
	d := s.m.backoffMin
	for i := 1; i < s.fails && d < s.m.backoffMax; i++ {
		d *= 2
	}
	d = min(d, s.m.backoffMax)
	j := int64(d) / 10
	if j > 0 {
		d += time.Duration(rand.Int64N(2*j+1) - j)
	}
	return d
}

func (s *source) backgroundDue(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	if s.cred == nil || s.inflight != nil || now.Before(s.nextTry) {
		return false
	}
	return !s.freshFor(s.m.bgWindow - s.jitter)
}

func (s *source) Meta() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	md := map[string]string{}
	if s.lastErr != "" {
		md["last_error"] = s.lastErr
	}
	c := s.cred
	if c == nil {
		md["status"] = "not logged in"
		return md
	}
	md["type"] = c.Type
	set := func(k, v string) {
		if v != "" {
			md[k] = v
		}
	}
	set("email", c.Email)
	set("plan", c.Plan)
	if !c.ExpiresAt.IsZero() {
		md["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if !c.LastRefresh.IsZero() {
		md["last_refresh"] = c.LastRefresh.UTC().Format(time.RFC3339)
	}
	switch c.Type {
	case TypeCodex:
		set("header:Chatgpt-Account-Id", c.AccountID)
	case TypeCopilot:
		// GitHub may reject requests that do not carry an editor identity it
		// recognises; we only state the truth (clirouter) and do not claim to be an IDE.
		md["header:Editor-Version"] = "clirouter/" + Version
		set("base_url", c.Extra["api_base"])
	case TypeGeminiCLI:
		set("project", c.ProjectID)
	}
	return md
}
