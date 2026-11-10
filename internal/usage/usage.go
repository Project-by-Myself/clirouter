// Package usage keeps per-subject (client key or provider) usage counters in
// memory so budgets, rate limits and concurrency caps can be enforced on the
// hot path without touching SQLite. Counters are seeded from the store's
// daily rollup when a subject is configured and then updated per request.
package usage

import (
	"sync"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

// Totals is usage over some window.
type Totals struct {
	Requests int64   `json:"requests"`
	Input    int64   `json:"input_tokens"`
	Output   int64   `json:"output_tokens"`
	Cost     float64 `json:"cost_usd"`
}

func (t *Totals) add(o Totals) {
	t.Requests += o.Requests
	t.Input += o.Input
	t.Output += o.Output
	t.Cost += o.Cost
}

// Loader returns stored totals for a subject since a day (inclusive).
type Loader func(kind Kind, name string, since time.Time) Totals

type Kind byte

const (
	Key      Kind = 'k'
	Provider Kind = 'p'
)

type id struct {
	kind Kind
	name string
}

type subject struct {
	period     string
	start, end time.Time // end is zero for PeriodTotal
	cur        Totals
	dayStart   time.Time
	today      Totals
	inflight   int
	recent     []time.Time // admitted request times within the last minute
	lastUsed   time.Time
}

// Tracker is safe for concurrent use.
type Tracker struct {
	mu   sync.Mutex
	subj map[id]*subject
	load Loader
	now  func() time.Time
}

func New(load Loader) *Tracker {
	return &Tracker{subj: map[id]*subject{}, load: load, now: time.Now}
}

// Bounds returns the period containing now (local time). end is zero for
// PeriodTotal.
func Bounds(period string, now time.Time) (start, end time.Time) {
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch config.NormPeriod(period) {
	case config.PeriodDaily:
		return day, day.AddDate(0, 0, 1)
	case config.PeriodWeekly:
		wd := (int(day.Weekday()) + 6) % 7 // Monday = 0
		s := day.AddDate(0, 0, -wd)
		return s, s.AddDate(0, 0, 7)
	case config.PeriodTotal:
		return time.Time{}, time.Time{}
	default:
		s := time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
		return s, s.AddDate(0, 1, 0)
	}
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// Sync configures the subjects for cfg: new subjects or changed periods are
// (re)loaded from the store; removed ones are dropped. Called on config
// load, not on the hot path.
func (t *Tracker) Sync(cfg *config.Config) {
	want := map[id]string{}
	for _, k := range cfg.APIKeys {
		want[id{Key, k.Name}] = config.NormPeriod(k.BudgetPeriod)
	}
	if len(cfg.APIKeys) == 0 {
		want[id{Key, "local"}] = config.PeriodMonthly
	}
	for _, p := range cfg.Providers {
		want[id{Provider, p.ID}] = config.NormPeriod(p.BudgetPeriod)
	}
	now := t.now()
	type job struct {
		id     id
		period string
	}
	var jobs []job
	t.mu.Lock()
	for k := range t.subj {
		if _, ok := want[k]; !ok {
			delete(t.subj, k)
		}
	}
	for k, per := range want {
		if s, ok := t.subj[k]; !ok || s.period != per {
			jobs = append(jobs, job{k, per})
		}
	}
	t.mu.Unlock()
	// Load outside the lock: SQLite reads must not stall requests.
	for _, j := range jobs {
		start, end := Bounds(j.period, now)
		day := dayOf(now)
		s := &subject{period: j.period, start: start, end: end, dayStart: day}
		if t.load != nil {
			s.cur = t.load(j.id.kind, j.id.name, start)
			s.today = t.load(j.id.kind, j.id.name, day)
		}
		t.mu.Lock()
		if old, ok := t.subj[j.id]; ok {
			s.inflight, s.recent, s.lastUsed = old.inflight, old.recent, old.lastUsed
		}
		t.subj[j.id] = s
		t.mu.Unlock()
	}
}

// get returns the subject, rolling its windows over when needed. Caller holds mu.
func (t *Tracker) get(k id, now time.Time) *subject {
	s, ok := t.subj[k]
	if !ok {
		start, end := Bounds(config.PeriodMonthly, now)
		s = &subject{period: config.PeriodMonthly, start: start, end: end, dayStart: dayOf(now)}
		t.subj[k] = s
	}
	if !s.end.IsZero() && !now.Before(s.end) {
		s.start, s.end = Bounds(s.period, now)
		s.cur = Totals{}
	}
	if d := dayOf(now); !d.Equal(s.dayStart) {
		s.dayStart, s.today = d, Totals{}
	}
	return s
}

// Add records a finished request.
func (t *Tracker) Add(kind Kind, name string, u Totals) {
	now := t.now()
	t.mu.Lock()
	s := t.get(id{kind, name}, now)
	s.cur.add(u)
	s.today.add(u)
	s.lastUsed = now
	t.mu.Unlock()
}

// OverBudget reports whether the current period's usage reached a budget
// (0 = unlimited).
func (t *Tracker) OverBudget(kind Kind, name string, usd float64, tokens int64) bool {
	if usd <= 0 && tokens <= 0 {
		return false
	}
	t.mu.Lock()
	s := t.get(id{kind, name}, t.now())
	cur := s.cur
	t.mu.Unlock()
	return (usd > 0 && cur.Cost >= usd) || (tokens > 0 && cur.Input+cur.Output >= tokens)
}

// Acquire admits a request under rpm and maxParallel (0 = unlimited). On
// success the caller must call release when the request ends. Otherwise
// retryAfter tells the client when to retry.
func (t *Tracker) Acquire(kind Kind, name string, rpm, maxParallel int) (release func(), retryAfter time.Duration, ok bool) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.get(id{kind, name}, now)
	cut := now.Add(-time.Minute)
	i := 0
	for i < len(s.recent) && !s.recent[i].After(cut) {
		i++
	}
	s.recent = s.recent[i:]
	if rpm > 0 && len(s.recent) >= rpm {
		return nil, s.recent[0].Add(time.Minute).Sub(now) + time.Second, false
	}
	if maxParallel > 0 && s.inflight >= maxParallel {
		return nil, time.Second, false
	}
	s.recent = append(s.recent, now)
	s.inflight++
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			s.inflight--
			t.mu.Unlock()
		})
	}, 0, true
}

// Snapshot is a subject's current state.
type Snapshot struct {
	Period      string     `json:"period"`
	PeriodStart time.Time  `json:"period_start"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
	Totals
	Today    Totals     `json:"today"`
	Inflight int        `json:"inflight"`
	RPMNow   int        `json:"rpm_now"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

func (t *Tracker) Snapshot(kind Kind, name string) Snapshot {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.get(id{kind, name}, now)
	snap := Snapshot{Period: s.period, PeriodStart: s.start, Totals: s.cur, Today: s.today, Inflight: s.inflight}
	if !s.end.IsZero() {
		e := s.end
		snap.PeriodEnd = &e
	}
	cut := now.Add(-time.Minute)
	for _, r := range s.recent {
		if r.After(cut) {
			snap.RPMNow++
		}
	}
	if !s.lastUsed.IsZero() {
		lu := s.lastUsed
		snap.LastUsed = &lu
	}
	return snap
}
