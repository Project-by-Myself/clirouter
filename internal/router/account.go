package router

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/redact"
)

type Account struct {
	ID       string
	Label    string
	Provider *Provider
	OAuth    TokenSource
	apiKey   string
	inflight atomic.Int64
	st       *state
}

func (a *Account) APIKey() string { return a.apiKey }

func (a *Account) Begin() { a.inflight.Add(1) }
func (a *Account) End()   { a.inflight.Add(-1) }

type FailureKind uint8

const (
	FailRateLimit FailureKind = iota // 429: scoped to the model unless the quota is account-wide
	FailQuota                        // quota/billing exhausted for the whole account
	FailAuth                         // 401/403 that a refresh could not fix
	FailModel                        // model not found / not enabled on this account
	FailServer                       // 5xx / overloaded
	FailNetwork                      // dial, TLS, first-byte timeout, reset
)

type state struct {
	mu         sync.Mutex
	until      time.Time
	modelUntil map[string]time.Time
	fails      int
	lastErr    string
	lastErrAt  time.Time
	successes  int64
	failures   int64
	lastUsed   time.Time
	quota      []QuotaWindow
	quotaAt    time.Time
	quotaErr   string
}

func newState() *state { return &state{modelUntil: map[string]time.Time{}} }

func (s *state) wait(model string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.until.Sub(now)
	if mu, ok := s.modelUntil[model]; ok {
		if mw := mu.Sub(now); mw > w {
			w = mw
		}
	}
	return w
}

func (a *Account) MarkSuccess(model string) {
	s := a.st
	s.mu.Lock()
	s.fails = 0
	s.successes++
	s.lastUsed = time.Now()
	delete(s.modelUntil, model)
	s.mu.Unlock()
}

// MarkFailure applies a cooldown. retryAfter > 0 is an upstream hint and
// takes precedence over the backoff ladder.
func (a *Account) MarkFailure(model string, kind FailureKind, retryAfter time.Duration, msg string) {
	s := a.st
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails++
	s.failures++
	s.lastErr = redact.String(msg)
	s.lastErrAt = now
	s.lastUsed = now
	var d time.Duration
	modelScoped := false
	switch kind {
	case FailRateLimit:
		modelScoped = true
		d = retryAfter
		if d <= 0 {
			d = ladder(s.fails, 2*time.Second, 5*time.Minute)
		}
	case FailQuota:
		d = retryAfter
		if d <= 0 {
			d = time.Hour
		}
	case FailAuth:
		d = 30 * time.Minute
	case FailModel:
		modelScoped = true
		d = time.Hour
	case FailServer, FailNetwork:
		// A single transient error does not bench the account; repeated ones do.
		if s.fails >= 3 {
			d = ladder(s.fails-2, 5*time.Second, time.Minute)
		}
		if retryAfter > d {
			d = retryAfter
		}
	}
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	if d <= 0 {
		return
	}
	if modelScoped {
		s.modelUntil[model] = now.Add(d)
	} else {
		s.until = now.Add(d)
	}
}

func ladder(n int, base, cap time.Duration) time.Duration {
	d := base
	for i := 1; i < n && d < cap; i++ {
		d *= 2
	}
	return min(d, cap)
}

// Reset clears all cooldowns (dashboard action).
func (a *Account) Reset() {
	s := a.st
	s.mu.Lock()
	s.until = time.Time{}
	s.modelUntil = map[string]time.Time{}
	s.fails = 0
	s.mu.Unlock()
}

type AccountStatus struct {
	ID            string            `json:"id"`
	Provider      string            `json:"provider"`
	Label         string            `json:"label"`
	Kind          string            `json:"kind"`
	Inflight      int64             `json:"inflight"`
	CooldownUntil *time.Time        `json:"cooldown_until,omitempty"`
	ModelCooldown map[string]string `json:"model_cooldown,omitempty"`
	Successes     int64             `json:"successes"`
	Failures      int64             `json:"failures"`
	LastError     string            `json:"last_error,omitempty"`
	LastErrorAt   *time.Time        `json:"last_error_at,omitempty"`
	LastUsed      *time.Time        `json:"last_used,omitempty"`
	Meta          map[string]string `json:"meta,omitempty"`
	Quota         []QuotaWindow     `json:"quota,omitempty"`
	QuotaAt       *time.Time        `json:"quota_at,omitempty"`
	QuotaError    string            `json:"quota_error,omitempty"`
}

func (a *Account) Status() AccountStatus {
	s := a.st
	now := time.Now()
	st := AccountStatus{ID: a.ID, Provider: a.Provider.Cfg.ID, Label: a.Label, Kind: "api_key", Inflight: a.inflight.Load()}
	if a.OAuth != nil {
		st.Kind = "oauth"
		st.Meta = a.OAuth.Meta()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.until.After(now) {
		t := s.until
		st.CooldownUntil = &t
	}
	for m, u := range s.modelUntil {
		if u.After(now) {
			if st.ModelCooldown == nil {
				st.ModelCooldown = map[string]string{}
			}
			st.ModelCooldown[m] = u.Format(time.RFC3339)
		}
	}
	st.Successes, st.Failures, st.LastError = s.successes, s.failures, s.lastErr
	if !s.lastErrAt.IsZero() {
		t := s.lastErrAt
		st.LastErrorAt = &t
	}
	if !s.lastUsed.IsZero() {
		t := s.lastUsed
		st.LastUsed = &t
	}
	if len(s.quota) > 0 {
		st.Quota = append([]QuotaWindow(nil), s.quota...)
		t := s.quotaAt
		st.QuotaAt = &t
	}
	st.QuotaError = s.quotaErr
	return st
}
