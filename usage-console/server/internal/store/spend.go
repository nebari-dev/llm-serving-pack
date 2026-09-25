package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// SpendCell is settled spend over a window for one team, key, model and
// backend. Callers group cells into whatever dimension they chart.
type SpendCell struct {
	Team     string
	KeyID    string
	Model    string
	Backend  string
	USD      float64
	Requests int
	Tokens   int
}

const day = 24 * time.Hour

// spendSource reads spend over [from, to) from the aggregates: whole UTC days
// from receipts_daily, and the partial days at either edge from receipts_5m.
// It matches summing receipts_5m over the window, as Overview does, while
// scanning a daily row instead of 288 five-minute ones for each full day. $1
// is the tenant; the bounds are $2 to $5.
const spendSource = `(
	SELECT bucket, team, key_id, resolved_model, backend, cost_usd, requests, tokens FROM receipts_daily
	WHERE tenant_id = $1 AND bucket >= $4 AND bucket < $5
	UNION ALL
	SELECT bucket, team, key_id, resolved_model, backend, cost_usd, requests, tokens FROM receipts_5m
	WHERE tenant_id = $1 AND bucket >= $2 AND bucket < $3 AND NOT (bucket >= $4 AND bucket < $5)
) s`

// DaySpan is the whole UTC days inside [from, to). It's empty (start >= end)
// when the window holds no full day.
func DaySpan(from, to time.Time) (start, end time.Time) {
	start = from.UTC().Truncate(day)
	if start.Before(from) {
		start = start.Add(day)
	}
	return start, to.UTC().Truncate(day)
}

func spendArgs(tenant string, from, to time.Time) []any {
	start, end := DaySpan(from, to)
	return []any{tenant, from, to, start, end}
}

// SpendCells sums spend over [from, to) by team, key, model and backend.
func (s *Store) SpendCells(ctx context.Context, tenant string, from, to time.Time) ([]SpendCell, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT team, key_id, resolved_model, backend,
		       coalesce(sum(cost_usd), 0)::float8, sum(requests)::int, coalesce(sum(tokens), 0)::int
		FROM `+spendSource+` GROUP BY 1, 2, 3, 4`, spendArgs(tenant, from, to)...)
	return collect(rows, func(r pgx.Rows) (SpendCell, error) {
		var c SpendCell
		return c, r.Scan(&c.Team, &c.KeyID, &c.Model, &c.Backend, &c.USD, &c.Requests, &c.Tokens)
	})
}

// SpendBucket is one cell's spend within one chart bucket.
type SpendBucket struct {
	Start time.Time
	SpendCell
}

// SpendBuckets is SpendCells split into buckets of width `bucket`, which must
// be a multiple of 5 minutes, or a whole day when from is a UTC midnight.
func (s *Store) SpendBuckets(ctx context.Context, tenant string, from, to time.Time, bucket time.Duration) ([]SpendBucket, error) {
	args := append(spendArgs(tenant, from, to), bucket)
	rows, _ := s.Receipts.Query(ctx, `
		SELECT time_bucket($6::interval, bucket) AS b, team, key_id, resolved_model, backend,
		       coalesce(sum(cost_usd), 0)::float8, sum(requests)::int, coalesce(sum(tokens), 0)::int
		FROM `+spendSource+` GROUP BY 1, 2, 3, 4, 5`, args...)
	return collect(rows, func(r pgx.Rows) (SpendBucket, error) {
		var b SpendBucket
		return b, r.Scan(&b.Start, &b.Team, &b.KeyID, &b.Model, &b.Backend, &b.USD, &b.Requests, &b.Tokens)
	})
}

// FirstSpendDay is the earliest UTC day with settled receipts, or the zero
// time when there are none.
func (s *Store) FirstSpendDay(ctx context.Context, tenant string) (time.Time, error) {
	var t *time.Time
	err := s.Receipts.QueryRow(ctx, `SELECT min(bucket) FROM receipts_daily WHERE tenant_id = $1`, tenant).Scan(&t)
	if t == nil {
		return time.Time{}, err
	}
	return t.UTC(), err
}
