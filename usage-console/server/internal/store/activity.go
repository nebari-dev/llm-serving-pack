package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ActivityBucket is one backend's traffic in one 5-minute bucket of
// receipts_5m. Errors are 5xx and 429 responses; Served is what was neither
// blocked nor an error, so cost per request isn't diluted by requests that
// never reached a model. Unpriced are served requests with no cost yet,
// which receipts_5m can't tell from free ones: the caller fills it in from
// UnpricedCells.
type ActivityBucket struct {
	Start           time.Time
	Backend         string
	Requests        int
	Served          int
	Errors          int
	BlockedRedacted int
	Unpriced        int
	CostUSD         float64
}

// ActivityBuckets reads receipts_5m over [from, to) by bucket and backend.
func (s *Store) ActivityBuckets(ctx context.Context, tenant string, from, to time.Time) ([]ActivityBucket, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT bucket, backend, sum(requests)::int,
		       coalesce(sum(requests - errors) FILTER (WHERE verdict <> 'blocked'), 0)::int,
		       sum(errors)::int,
		       coalesce(sum(requests) FILTER (WHERE verdict IN ('blocked', 'redacted')), 0)::int,
		       coalesce(sum(cost_usd), 0)::float8
		FROM receipts_5m WHERE tenant_id = $1 AND bucket >= $2 AND bucket < $3
		GROUP BY 1, 2 ORDER BY 1`, tenant, from, to)
	return collect(rows, func(r pgx.Rows) (ActivityBucket, error) {
		var b ActivityBucket
		return b, r.Scan(&b.Start, &b.Backend, &b.Requests, &b.Served, &b.Errors, &b.BlockedRedacted, &b.CostUSD)
	})
}

// ScopeSpend is spend by team and key in one bucket of receipts_5m.
type ScopeSpend struct {
	Start time.Time
	Team  string
	KeyID string
	USD   float64
}

// ScopeSpendBuckets sums receipts_5m spend over [from, to) into buckets of
// width `bucket` (a multiple of 5 minutes), by team and key.
func (s *Store) ScopeSpendBuckets(ctx context.Context, tenant string, from, to time.Time, bucket time.Duration) ([]ScopeSpend, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT time_bucket($4::interval, bucket), team, key_id, coalesce(sum(cost_usd), 0)::float8
		FROM receipts_5m WHERE tenant_id = $1 AND bucket >= $2 AND bucket < $3
		GROUP BY 1, 2, 3 ORDER BY 1`, tenant, from, to, bucket)
	return collect(rows, func(r pgx.Rows) (ScopeSpend, error) {
		var c ScopeSpend
		return c, r.Scan(&c.Start, &c.Team, &c.KeyID, &c.USD)
	})
}
