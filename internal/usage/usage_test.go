package usage

import (
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

func TestBounds(t *testing.T) {
	loc := time.FixedZone("ICT", 7*3600)
	now := time.Date(2026, 11, 12, 15, 4, 0, 0, loc) // Thursday
	s, e := Bounds("weekly", now)
	if s != time.Date(2026, 11, 9, 0, 0, 0, 0, loc) || e != time.Date(2026, 11, 16, 0, 0, 0, 0, loc) {
		t.Errorf("weekly %v %v", s, e)
	}
	s, e = Bounds("", now)
	if s != time.Date(2026, 11, 1, 0, 0, 0, 0, loc) || e != time.Date(2026, 12, 1, 0, 0, 0, 0, loc) {
		t.Errorf("monthly %v %v", s, e)
	}
	if s, e = Bounds("total", now); !s.IsZero() || !e.IsZero() {
		t.Errorf("total %v %v", s, e)
	}
}

func TestBudgetRolloverAndLimits(t *testing.T) {
	now := time.Date(2026, 11, 30, 23, 0, 0, 0, time.Local)
	seeded := 0
	tr := New(func(k Kind, name string, since time.Time) Totals {
		seeded++
		return Totals{Cost: 4}
	})
	tr.now = func() time.Time { return now }
	tr.Sync(&config.Config{APIKeys: []config.ClientKey{{Name: "a", BudgetPeriod: "daily"}}})
	if seeded != 2 {
		t.Fatalf("loader calls = %d", seeded)
	}
	tr.Add(Key, "a", Totals{Requests: 1, Cost: 1.5})
	if !tr.OverBudget(Key, "a", 5, 0) {
		t.Fatal("4 + 1.5 should exceed 5")
	}
	now = now.Add(2 * time.Hour) // next day: new period
	if tr.OverBudget(Key, "a", 5, 0) {
		t.Fatal("budget did not reset")
	}
	// RPM and parallel caps.
	r1, _, ok := tr.Acquire(Key, "a", 2, 1)
	if !ok {
		t.Fatal("first")
	}
	if _, wait, ok := tr.Acquire(Key, "a", 2, 1); ok || wait <= 0 {
		t.Fatal("parallel cap not applied")
	}
	r1()
	r1() // idempotent
	r2, _, ok := tr.Acquire(Key, "a", 2, 1)
	if !ok {
		t.Fatal("second")
	}
	r2()
	if _, wait, ok := tr.Acquire(Key, "a", 2, 0); ok || wait < time.Minute-time.Second {
		t.Fatalf("rpm cap not applied: ok=%v wait=%v", ok, wait)
	}
	now = now.Add(61 * time.Second)
	if _, _, ok := tr.Acquire(Key, "a", 2, 0); !ok {
		t.Fatal("rpm window did not slide")
	}
	if snap := tr.Snapshot(Key, "a"); snap.Inflight != 1 || snap.Period != "daily" {
		t.Fatalf("snapshot %+v", snap)
	}
}
