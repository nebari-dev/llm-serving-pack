package store

import (
	"context"
	"time"
)

// ModeCount is how many recent receipts Warden handled in one policy mode.
type ModeCount struct {
	Mode  string
	Count int
	Since time.Time // the earliest of them
}

// RecentPolicyModes counts receipts since a time that Warden didn't enforce
// normally: pass-through, fail-open or fail-closed.
func (s *Store) RecentPolicyModes(ctx context.Context, tenant string, since time.Time) ([]ModeCount, error) {
	rows, err := s.Receipts.Query(ctx, `
		SELECT policy_mode, count(*), min(ts) FROM receipts
		WHERE tenant_id = $1 AND ts >= $2 AND policy_mode IN ('passthrough', 'fail-open', 'fail-closed')
		GROUP BY policy_mode`, tenant, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModeCount
	for rows.Next() {
		var m ModeCount
		if err := rows.Scan(&m.Mode, &m.Count, &m.Since); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// BackendFailures is one backend's settled requests since a time, and how
// many of them failed upstream (5xx, or a provider 429).
type BackendFailures struct {
	Backend   string
	Total     int
	Failed    int
	TopStatus int // the most common failing status
}

func (s *Store) RecentBackendFailures(ctx context.Context, tenant string, since time.Time) ([]BackendFailures, error) {
	rows, err := s.Receipts.Query(ctx, `
		SELECT backend, count(*),
		       count(*) FILTER (WHERE error_code IN ('upstream_error', 'upstream_rate_limited')),
		       coalesce(mode() WITHIN GROUP (ORDER BY status) FILTER (WHERE error_code IN ('upstream_error', 'upstream_rate_limited')), 0)
		FROM receipts
		WHERE tenant_id = $1 AND ts >= $2 AND NOT in_flight AND backend NOT IN ('', '—')
		GROUP BY backend`, tenant, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackendFailures
	for rows.Next() {
		var b BackendFailures
		if err := rows.Scan(&b.Backend, &b.Total, &b.Failed, &b.TopStatus); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Overhead is the gateway's own time on requests (spec G6), over a window.
type Overhead struct {
	P50MS, P95MS *float64
	Samples      int
}

// GatewayOverhead is the p50 and p95 of receipts' overhead_us since `since`,
// in milliseconds; nil with no samples.
func (s *Store) GatewayOverhead(ctx context.Context, tenant string, since time.Time) (Overhead, error) {
	var o Overhead
	err := s.Receipts.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY overhead_us) / 1000.0,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY overhead_us) / 1000.0,
		       count(overhead_us)::int
		FROM receipts
		WHERE tenant_id = $1 AND ts >= $2 AND overhead_us IS NOT NULL`, tenant, since).Scan(&o.P50MS, &o.P95MS, &o.Samples)
	return o, err
}
