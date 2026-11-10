// Package store persists request records off the hot path: the proxy hands
// records to a bounded channel (never blocking; drops are counted) and one
// writer goroutine batches them into SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Record struct {
	ID            int64     `json:"id"`
	TS            time.Time `json:"ts"`
	ClientKey     string    `json:"client_key"`
	ClientFormat  string    `json:"client_format"`
	Model         string    `json:"model"`
	Provider      string    `json:"provider"`
	Account       string    `json:"account"`
	UpstreamModel string    `json:"upstream_model"`
	Status        int       `json:"status"`
	Stream        bool      `json:"stream"`
	Passthrough   bool      `json:"passthrough"`
	Attempts      int       `json:"attempts"`
	TTFBms        int64     `json:"ttfb_ms"`
	DurationMs    int64     `json:"duration_ms"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	CacheRead     int64     `json:"cache_read_tokens"`
	CacheWrite    int64     `json:"cache_write_tokens"`
	Reasoning     int64     `json:"reasoning_tokens"`
	Error         string    `json:"error,omitempty"`
}

type Store struct {
	db      *sql.DB
	ch      chan Record
	dropped atomic.Int64
	ring    *ring
	done    chan struct{}
	wg      sync.WaitGroup
	retain  time.Duration
}

const schema = `
CREATE TABLE IF NOT EXISTS requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  client_key TEXT, client_format TEXT, model TEXT, provider TEXT, account TEXT, upstream_model TEXT,
  status INTEGER, stream INTEGER, passthrough INTEGER, attempts INTEGER,
  ttfb_ms INTEGER, duration_ms INTEGER,
  input_tokens INTEGER, output_tokens INTEGER, cache_read INTEGER, cache_write INTEGER, reasoning INTEGER,
  error TEXT
);
CREATE INDEX IF NOT EXISTS requests_ts ON requests(ts);
`

func Open(dir string, retainDays int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "clirouter.db") + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if retainDays <= 0 {
		retainDays = 14
	}
	s := &Store{db: db, ch: make(chan Record, 8192), ring: newRing(1000), done: make(chan struct{}), retain: time.Duration(retainDays) * 24 * time.Hour}
	s.wg.Add(1)
	go s.run()
	return s, nil
}

// Add never blocks the caller.
func (s *Store) Add(r Record) {
	s.ring.add(r)
	select {
	case s.ch <- r:
	default:
		s.dropped.Add(1)
	}
}

func (s *Store) Dropped() int64 { return s.dropped.Load() }

func (s *Store) Close() error {
	close(s.done)
	s.wg.Wait()
	return s.db.Close()
}

func (s *Store) run() {
	defer s.wg.Done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	batch := make([]Record, 0, 256)
	flush := func() {
		if len(batch) > 0 {
			_ = s.insert(batch)
			batch = batch[:0]
		}
	}
	s.prune()
	for {
		select {
		case r := <-s.ch:
			batch = append(batch, r)
			if len(batch) >= 256 {
				flush()
			}
		case <-tick.C:
			flush()
		case <-prune.C:
			s.prune()
		case <-s.done:
			for {
				select {
				case r := <-s.ch:
					batch = append(batch, r)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *Store) insert(rs []Record) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO requests (ts,client_key,client_format,model,provider,account,upstream_model,status,stream,passthrough,attempts,ttfb_ms,duration_ms,input_tokens,output_tokens,cache_read,cache_write,reasoning,error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, r := range rs {
		if _, err := st.Exec(r.TS.UnixMilli(), r.ClientKey, r.ClientFormat, r.Model, r.Provider, r.Account, r.UpstreamModel, r.Status, b2i(r.Stream), b2i(r.Passthrough), r.Attempts, r.TTFBms, r.DurationMs, r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite, r.Reasoning, r.Error); err != nil {
			st.Close()
			tx.Rollback()
			return err
		}
	}
	st.Close()
	return tx.Commit()
}

func (s *Store) prune() {
	_, _ = s.db.Exec(`DELETE FROM requests WHERE ts < ?`, time.Now().Add(-s.retain).UnixMilli())
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Live returns the most recent records from memory (newest first).
func (s *Store) Live(n int) []Record { return s.ring.last(n) }

type Query struct {
	Limit    int
	BeforeID int64
	Provider string
	Model    string
	Errors   bool
}

func (s *Store) Requests(ctx context.Context, q Query) ([]Record, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	var where []string
	var args []any
	if q.BeforeID > 0 {
		where, args = append(where, "id < ?"), append(args, q.BeforeID)
	}
	if q.Provider != "" {
		where, args = append(where, "provider = ?"), append(args, q.Provider)
	}
	if q.Model != "" {
		where, args = append(where, "model = ?"), append(args, q.Model)
	}
	if q.Errors {
		where = append(where, "status >= 400")
	}
	sqlq := `SELECT id,ts,client_key,client_format,model,provider,account,upstream_model,status,stream,passthrough,attempts,ttfb_ms,duration_ms,input_tokens,output_tokens,cache_read,cache_write,reasoning,error FROM requests`
	if len(where) > 0 {
		sqlq += " WHERE " + strings.Join(where, " AND ")
	}
	sqlq += " ORDER BY id DESC LIMIT ?"
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var ts int64
		var stream, pass int
		var errs sql.NullString
		if err := rows.Scan(&r.ID, &ts, &r.ClientKey, &r.ClientFormat, &r.Model, &r.Provider, &r.Account, &r.UpstreamModel, &r.Status, &stream, &pass, &r.Attempts, &r.TTFBms, &r.DurationMs, &r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheWrite, &r.Reasoning, &errs); err != nil {
			return nil, err
		}
		r.TS = time.UnixMilli(ts)
		r.Stream, r.Passthrough, r.Error = stream == 1, pass == 1, errs.String
		out = append(out, r)
	}
	return out, rows.Err()
}

type Bucket struct {
	Key          string  `json:"key"`
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CacheRead    int64   `json:"cache_read_tokens"`
	AvgTTFBms    float64 `json:"avg_ttfb_ms"`
	P95TTFBms    int64   `json:"p95_ttfb_ms"`
}

type Stats struct {
	Since      time.Time `json:"since"`
	Total      Bucket    `json:"total"`
	ByModel    []Bucket  `json:"by_model"`
	ByProvider []Bucket  `json:"by_provider"`
	ByKey      []Bucket  `json:"by_key"`
	Timeline   []Bucket  `json:"timeline"` // key = bucket start (unix ms)
}

func (s *Store) Stats(ctx context.Context, since time.Time) (*Stats, error) {
	st := &Stats{Since: since}
	ms := since.UnixMilli()
	agg := `COUNT(*), SUM(status>=400), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read),0), COALESCE(AVG(CASE WHEN status<400 THEN ttfb_ms END),0)`
	scan := func(q string, args ...any) ([]Bucket, error) {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Bucket
		for rows.Next() {
			var b Bucket
			var errs sql.NullInt64
			if err := rows.Scan(&b.Key, &b.Requests, &errs, &b.InputTokens, &b.OutputTokens, &b.CacheRead, &b.AvgTTFBms); err != nil {
				return nil, err
			}
			b.Errors = errs.Int64
			out = append(out, b)
		}
		return out, rows.Err()
	}
	tot, err := scan(`SELECT 'total', `+agg+` FROM requests WHERE ts >= ?`, ms)
	if err != nil {
		return nil, err
	}
	if len(tot) > 0 {
		st.Total = tot[0]
	}
	var p95 sql.NullInt64
	_ = s.db.QueryRowContext(ctx, `SELECT ttfb_ms FROM requests WHERE ts >= ? AND status < 400 ORDER BY ttfb_ms LIMIT 1 OFFSET (SELECT CAST(COUNT(*)*0.95 AS INTEGER) FROM requests WHERE ts >= ? AND status < 400)`, ms, ms).Scan(&p95)
	st.Total.P95TTFBms = p95.Int64
	if st.ByModel, err = scan(`SELECT model, `+agg+` FROM requests WHERE ts >= ? GROUP BY model ORDER BY 2 DESC LIMIT 50`, ms); err != nil {
		return nil, err
	}
	if st.ByProvider, err = scan(`SELECT provider, `+agg+` FROM requests WHERE ts >= ? GROUP BY provider ORDER BY 2 DESC LIMIT 50`, ms); err != nil {
		return nil, err
	}
	if st.ByKey, err = scan(`SELECT client_key, `+agg+` FROM requests WHERE ts >= ? GROUP BY client_key ORDER BY 2 DESC LIMIT 50`, ms); err != nil {
		return nil, err
	}
	span := time.Since(since)
	step := int64(time.Hour / time.Millisecond)
	if span <= 6*time.Hour {
		step = int64(5 * time.Minute / time.Millisecond)
	} else if span > 3*24*time.Hour {
		step = int64(24 * time.Hour / time.Millisecond)
	}
	st.Timeline, err = scan(fmt.Sprintf(`SELECT CAST((ts/%d)*%d AS TEXT), %s FROM requests WHERE ts >= ? GROUP BY ts/%d ORDER BY 1`, step, step, agg, step), ms)
	return st, err
}

type ring struct {
	mu  sync.Mutex
	buf []Record
	pos int
	n   int
	seq int64
}

func newRing(n int) *ring { return &ring{buf: make([]Record, n)} }

func (r *ring) add(rec Record) {
	r.mu.Lock()
	r.seq++
	rec.ID = -r.seq
	r.buf[r.pos] = rec
	r.pos = (r.pos + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
	r.mu.Unlock()
}

func (r *ring) last(n int) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > r.n {
		n = r.n
	}
	out := make([]Record, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, r.buf[(r.pos-i+len(r.buf))%len(r.buf)])
	}
	return out
}
