package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// WindowTotals sums settled receipts over [from, to) from the 5-minute
// aggregate, which includes the not-yet-materialized tail. SpendUSD leaves
// out the Unpriced requests, which have no price yet.
type WindowTotals struct {
	Requests int     `json:"requests"`
	Blocked  int     `json:"blocked"`
	Redacted int     `json:"redacted"`
	SpendUSD float64 `json:"spendUsd"`
	Unpriced int     `json:"unpriced"`
}

func (s *Store) Totals(ctx context.Context, tenant string, from, to time.Time) (WindowTotals, error) {
	var w WindowTotals
	var err error
	if w.Unpriced, err = s.UnpricedCount(ctx, tenant, from, to); err != nil {
		return w, err
	}
	err = s.Receipts.QueryRow(ctx, `
		SELECT coalesce(sum(requests), 0)::int,
		       coalesce(sum(requests) FILTER (WHERE verdict = 'blocked'), 0)::int,
		       coalesce(sum(requests) FILTER (WHERE verdict = 'redacted'), 0)::int,
		       coalesce(sum(cost_usd), 0)::float8
		FROM receipts_5m WHERE tenant_id = $1 AND bucket >= $2 AND bucket < $3`, tenant, from, to).
		Scan(&w.Requests, &w.Blocked, &w.Redacted, &w.SpendUSD)
	return w, err
}

// SpendBy sums spend over [from, to) by team, and by key and model.
type SpendBy struct {
	Team     map[string]float64
	Key      map[string]float64
	KeyModel map[[2]string]float64
}

func (s *Store) SpendBy(ctx context.Context, tenant string, from, to time.Time) (SpendBy, error) {
	out := SpendBy{Team: map[string]float64{}, Key: map[string]float64{}, KeyModel: map[[2]string]float64{}}
	rows, _ := s.Receipts.Query(ctx, `
		SELECT team, key_id, resolved_model, coalesce(sum(cost_usd), 0)::float8
		FROM receipts_5m WHERE tenant_id = $1 AND bucket >= $2 AND bucket < $3
		GROUP BY 1, 2, 3`, tenant, from, to)
	_, err := collect(rows, func(r pgx.Rows) (struct{}, error) {
		var team, key, m string
		var usd float64
		if err := r.Scan(&team, &key, &m, &usd); err != nil {
			return struct{}{}, err
		}
		out.Team[team] += usd
		out.Key[key] += usd
		out.KeyModel[[2]string{key, m}] += usd
		return struct{}{}, nil
	})
	return out, err
}

// Impact describes traffic over [from, to) from raw receipts, for comparing
// either side of a change. Latency and cost are over successful requests,
// cost over those with a price: null when none had one. Unpriced counts the
// successful ones without.
type Impact struct {
	Requests             int      `json:"requests"`
	P50MS                float64  `json:"p50Ms"`
	CostPerRequestUSD    *float64 `json:"costPerRequestUsd"`
	Unpriced             int      `json:"unpriced"`
	ErrorRate            float64  `json:"errorRate"`            // share, 0–1
	BlockedRedactedShare float64  `json:"blockedRedactedShare"` // share, 0–1
}

func (s *Store) Impact(ctx context.Context, tenant string, from, to time.Time) (Impact, error) {
	var im Impact
	err := s.Receipts.QueryRow(ctx, `
		SELECT count(*)::int,
		       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status = 200), 0)::float8,
		       (avg(cost_usd) FILTER (WHERE status = 200))::float8,
		       count(*) FILTER (WHERE status = 200 AND cost_usd IS NULL)::int,
		       coalesce(avg((coalesce(error_code, '') IN ('upstream_error', 'upstream_rate_limited'))::int), 0)::float8,
		       coalesce(avg((verdict IN ('blocked', 'redacted'))::int), 0)::float8
		FROM receipts WHERE tenant_id = $1 AND ts >= $2 AND ts < $3 AND NOT in_flight`, tenant, from, to).
		Scan(&im.Requests, &im.P50MS, &im.CostPerRequestUSD, &im.Unpriced, &im.ErrorRate, &im.BlockedRedactedShare)
	return im, err
}
