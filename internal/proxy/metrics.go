package proxy

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/Project-by-Myself/clirouter/internal/store"
)

var ttfbBuckets = []int64{100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000}

// Metrics are lock-free counters on the hot path plus a small mutex-guarded
// per-provider map; exposed in Prometheus text format.
type Metrics struct {
	inflight atomic.Int64
	total    atomic.Int64
	errors   atomic.Int64
	ttfb     [10]atomic.Int64 // len(ttfbBuckets)+1
	ttfbSum  atomic.Int64
	ttfbN    atomic.Int64
	tokIn    atomic.Int64
	tokOut   atomic.Int64

	mu         sync.Mutex
	byProvider map[string]*[2]int64 // requests, errors
}

func NewMetrics() *Metrics { return &Metrics{byProvider: map[string]*[2]int64{}} }

func (m *Metrics) Inflight() int64 { return m.inflight.Load() }

func (m *Metrics) observe(r *store.Record) {
	m.total.Add(1)
	isErr := r.Status >= 400 && r.Status != 499
	if isErr {
		m.errors.Add(1)
	}
	if r.TTFBms > 0 && !isErr {
		i := sort.Search(len(ttfbBuckets), func(i int) bool { return r.TTFBms <= ttfbBuckets[i] })
		m.ttfb[i].Add(1)
		m.ttfbSum.Add(r.TTFBms)
		m.ttfbN.Add(1)
	}
	m.tokIn.Add(r.InputTokens)
	m.tokOut.Add(r.OutputTokens)
	if r.Provider != "" {
		m.mu.Lock()
		c := m.byProvider[r.Provider]
		if c == nil {
			c = &[2]int64{}
			m.byProvider[r.Provider] = c
		}
		c[0]++
		if isErr {
			c[1]++
		}
		m.mu.Unlock()
	}
}

type Snapshot struct {
	Inflight     int64   `json:"inflight"`
	Total        int64   `json:"total"`
	Errors       int64   `json:"errors"`
	AvgTTFBms    float64 `json:"avg_ttfb_ms"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

func (m *Metrics) Snapshot() Snapshot {
	s := Snapshot{Inflight: m.inflight.Load(), Total: m.total.Load(), Errors: m.errors.Load(), InputTokens: m.tokIn.Load(), OutputTokens: m.tokOut.Load()}
	if n := m.ttfbN.Load(); n > 0 {
		s.AvgTTFBms = float64(m.ttfbSum.Load()) / float64(n)
	}
	return s
}

func (m *Metrics) WritePrometheus(w io.Writer) {
	fmt.Fprintf(w, "# TYPE clirouter_inflight gauge\nclirouter_inflight %d\n", m.inflight.Load())
	fmt.Fprintf(w, "# TYPE clirouter_requests_total counter\nclirouter_requests_total %d\n", m.total.Load())
	fmt.Fprintf(w, "# TYPE clirouter_errors_total counter\nclirouter_errors_total %d\n", m.errors.Load())
	fmt.Fprintf(w, "# TYPE clirouter_tokens_total counter\nclirouter_tokens_total{dir=\"input\"} %d\nclirouter_tokens_total{dir=\"output\"} %d\n", m.tokIn.Load(), m.tokOut.Load())
	fmt.Fprintf(w, "# TYPE clirouter_ttfb_ms histogram\n")
	var cum int64
	for i, b := range ttfbBuckets {
		cum += m.ttfb[i].Load()
		fmt.Fprintf(w, "clirouter_ttfb_ms_bucket{le=\"%d\"} %d\n", b, cum)
	}
	cum += m.ttfb[len(ttfbBuckets)].Load()
	fmt.Fprintf(w, "clirouter_ttfb_ms_bucket{le=\"+Inf\"} %d\nclirouter_ttfb_ms_sum %d\nclirouter_ttfb_ms_count %d\n", cum, m.ttfbSum.Load(), m.ttfbN.Load())
	m.mu.Lock()
	defer m.mu.Unlock()
	fmt.Fprintf(w, "# TYPE clirouter_provider_requests_total counter\n")
	for p, c := range m.byProvider {
		fmt.Fprintf(w, "clirouter_provider_requests_total{provider=%q} %d\nclirouter_provider_errors_total{provider=%q} %d\n", p, c[0], p, c[1])
	}
}
