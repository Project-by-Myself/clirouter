package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UsageTotals is aggregated usage from the daily rollup.
type UsageTotals struct {
	Requests int64
	Input    int64
	Output   int64
	Cost     float64
}

// UsageSince sums the rollup for one client key (byProvider=false) or one
// provider since the local day containing since.
func (s *Store) UsageSince(ctx context.Context, byProvider bool, name string, since time.Time) (UsageTotals, error) {
	col := "client_key"
	if byProvider {
		col = "provider"
	}
	var t UsageTotals
	day := "0000-00-00"
	if !since.IsZero() {
		day = since.Local().Format(time.DateOnly)
	}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost),0) FROM usage_daily WHERE `+col+` = ? AND day >= ?`, name, day).Scan(&t.Requests, &t.Input, &t.Output, &t.Cost)
	return t, err
}

type UsageRow struct {
	Key        string  `json:"key"`
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	Cost       float64 `json:"cost_usd"`
}

type UsageDay struct {
	Day      string  `json:"day"`
	Requests int64   `json:"requests"`
	Input    int64   `json:"input_tokens"`
	Output   int64   `json:"output_tokens"`
	Cost     float64 `json:"cost_usd"`
}

type UsageReport struct {
	Since string     `json:"since"`
	Total UsageRow   `json:"total"`
	Rows  []UsageRow `json:"rows"`
	Daily []UsageDay `json:"daily"`
}

// Usage reports the rollup since a day, grouped by "key", "model" or
// "provider", optionally filtered to one client key.
func (s *Store) Usage(ctx context.Context, since time.Time, group, key string) (*UsageReport, error) {
	col := map[string]string{"key": "client_key", "model": "model", "provider": "provider"}[group]
	if col == "" {
		return nil, fmt.Errorf("group must be key, model or provider")
	}
	day := since.Local().Format(time.DateOnly)
	where, args := "day >= ?", []any{day}
	if key != "" {
		where, args = where+" AND client_key = ?", append(args, key)
	}
	agg := `COALESCE(SUM(requests),0), COALESCE(SUM(errors),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0), COALESCE(SUM(cost),0)`
	rep := &UsageReport{Since: day, Rows: []UsageRow{}, Daily: []UsageDay{}}
	scanRow := func(sc interface{ Scan(...any) error }) (UsageRow, error) {
		var r UsageRow
		err := sc.Scan(&r.Key, &r.Requests, &r.Errors, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite, &r.Cost)
		return r, err
	}
	tot, err := scanRow(s.db.QueryRowContext(ctx, `SELECT 'total', `+agg+` FROM usage_daily WHERE `+where, args...))
	if err != nil {
		return nil, err
	}
	rep.Total = tot
	rows, err := s.db.QueryContext(ctx, `SELECT `+col+`, `+agg+` FROM usage_daily WHERE `+where+` GROUP BY `+col+` ORDER BY 8 DESC, 2 DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rep.Rows = append(rep.Rows, r)
	}
	rows.Close()
	drows, err := s.db.QueryContext(ctx, `SELECT day, COALESCE(SUM(requests),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost),0) FROM usage_daily WHERE `+where+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	for drows.Next() {
		var d UsageDay
		if err := drows.Scan(&d.Day, &d.Requests, &d.Input, &d.Output, &d.Cost); err != nil {
			return nil, err
		}
		rep.Daily = append(rep.Daily, d)
	}
	return rep, drows.Err()
}

// CostFunc prices one request.
type CostFunc func(provider, upstreamModel, model string, in, out, cacheRead, cacheWrite int64) float64

// Backfill prices request history that predates cost tracking and builds
// the daily rollup from it. It only runs once, right after usage_daily was
// created, and only touches rows that existed at Open.
func (s *Store) Backfill(ctx context.Context, cost CostFunc) error {
	if !s.needBackfill {
		return nil
	}
	s.needBackfill = false
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, client_key, provider, upstream_model, model, status, input_tokens, output_tokens, cache_read, cache_write FROM requests WHERE id <= ?`, s.backfillMaxID)
	if err != nil {
		return err
	}
	type upd struct {
		id   int64
		cost float64
	}
	var upds []upd
	roll := map[rollKey]*rollVal{}
	for rows.Next() {
		var r Record
		var ts int64
		var key, prov, up, model sql.NullString
		if err := rows.Scan(&r.ID, &ts, &key, &prov, &up, &model, &r.Status, &r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheWrite); err != nil {
			rows.Close()
			return err
		}
		r.TS, r.ClientKey, r.Provider, r.UpstreamModel, r.Model = time.UnixMilli(ts), key.String, prov.String, up.String, model.String
		r.Cost = cost(r.Provider, r.UpstreamModel, r.Model, r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite)
		if r.Cost > 0 {
			upds = append(upds, upd{r.ID, r.Cost})
		}
		k := rollKey{r.TS.Local().Format(time.DateOnly), r.ClientKey, r.Provider, r.Model}
		v := roll[k]
		if v == nil {
			v = &rollVal{}
			roll[k] = v
		}
		v.add(r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`UPDATE requests SET cost = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, u := range upds {
		if _, err := st.Exec(u.cost, u.id); err != nil {
			st.Close()
			tx.Rollback()
			return err
		}
	}
	st.Close()
	if err := upsertRollup(tx, roll); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
