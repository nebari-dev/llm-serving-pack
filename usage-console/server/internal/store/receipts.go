package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
)

// NotifyChannel carries "<id> <ts-ms>" for every receipt insert or settle.
const NotifyChannel = "receipts"

var receiptCols = []string{
	"id", "ts", "tenant_id", "trace_id", "session_id", "duration_ms", "ttft_ms", "key_id", "key_name", "team", "project", "actor",
	"requested_model", "resolved_model", "backend", "provider", "region", "route_reason", "fallback_from",
	"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_tokens", "total_tokens", "cost_usd", "cost_basis", "cache_write_tokens",
	"verdict", "inbound_verdict", "redactions", "rules", "status", "error_code", "error_detail",
	"request_hash", "response_hash", "content_captured", "content", "in_flight", "route_trace", "policy_mode", "secret_id", "overhead_us", "project_id",
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func receiptValues(r *model.Receipt) []any {
	js := func(v any) []byte {
		if v == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		return b
	}
	var basis []byte
	if r.CostBasis != nil {
		basis = js(r.CostBasis)
	}
	return []any{
		r.ID, time.UnixMilli(r.TS), r.TenantID, r.TraceID, nullStr(r.SessionID), r.DurationMS, r.TTFTMS, r.KeyID, r.KeyName, r.Team, r.Project, nullStr(r.Actor),
		r.RequestedModel, r.ResolvedModel, r.Backend, r.Provider, r.Region, r.RouteReason, nullStr(r.FallbackFrom),
		r.InputTokens, r.CachedInputTokens, r.OutputTokens, r.ReasoningTokens, r.InputTokens + r.OutputTokens + r.ReasoningTokens, r.CostUSD, basis, r.CacheWriteTokens,
		r.Verdict, r.InboundVerdict, js(r.Redactions), js(r.Rules), r.Status, nullStr(r.ErrorCode), nullStr(r.ErrorDetail),
		r.RequestHash, r.ResponseHash, r.ContentCaptured, js(r.Content), r.InFlight, js(r.Trace), nullStr(r.PolicyMode), nullStr(r.SecretID), r.OverheadUS, nullStr(r.ProjectID),
	}
}

// PutReceipt upserts a receipt (in-flight first, settled later) and notifies
// listeners in the same transaction.
func (s *Store) PutReceipt(ctx context.Context, r *model.Receipt) error {
	vals := receiptValues(r)
	ph, set := "", ""
	for i, c := range receiptCols {
		if i > 0 {
			ph += ","
		}
		ph += fmt.Sprintf("$%d", i+1)
		if c != "id" && c != "ts" {
			if set != "" {
				set += ","
			}
			set += c + " = EXCLUDED." + c
		}
	}
	tx, err := s.Receipts.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cols := ""
	for i, c := range receiptCols {
		if i > 0 {
			cols += ","
		}
		cols += c
	}
	if _, err := tx.Exec(ctx, `INSERT INTO receipts (`+cols+`) VALUES (`+ph+`) ON CONFLICT (id, ts) DO UPDATE SET `+set, vals...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, fmt.Sprintf("%s %d", r.ID, r.TS)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CopyReceipts bulk-loads settled receipts without notifying (backfill).
func (s *Store) CopyReceipts(ctx context.Context, rs []*model.Receipt) error {
	_, err := s.Receipts.CopyFrom(ctx, pgx.Identifier{"receipts"}, receiptCols, pgx.CopyFromSlice(len(rs), func(i int) ([]any, error) {
		return receiptValues(rs[i]), nil
	}))
	return err
}

// RefreshAggregates materializes the continuous aggregates over a window,
// e.g. after a backfill.
func (s *Store) RefreshAggregates(ctx context.Context, from, to time.Time) error {
	for _, v := range []string{"receipts_5m", "receipts_daily"} {
		if _, err := s.Receipts.Exec(ctx, `CALL refresh_continuous_aggregate($1::regclass, $2::timestamptz, $3::timestamptz)`, v, from, to); err != nil {
			return fmt.Errorf("%s: %w", v, err)
		}
	}
	return nil
}

const selectReceipt = `SELECT id, ts, tenant_id, trace_id, coalesce(session_id, ''), duration_ms, ttft_ms, key_id, key_name, team, project, coalesce(actor, ''),
	requested_model, resolved_model, backend, provider, region, route_reason, coalesce(fallback_from, ''),
	input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, cost_usd::float8,
	verdict, inbound_verdict, redactions, rules, status, coalesce(error_code, ''), coalesce(error_detail, ''),
	request_hash, response_hash, content_captured, in_flight, route_trace, coalesce(policy_mode, ''), cost_basis, coalesce(secret_id, ''), cache_write_tokens, overhead_us, coalesce(project_id, '') FROM receipts`

func scanReceipt(row pgx.Row) (model.Receipt, error) {
	var r model.Receipt
	var ts time.Time
	var red, rules, trace, basis []byte
	err := row.Scan(&r.ID, &ts, &r.TenantID, &r.TraceID, &r.SessionID, &r.DurationMS, &r.TTFTMS, &r.KeyID, &r.KeyName, &r.Team, &r.Project, &r.Actor,
		&r.RequestedModel, &r.ResolvedModel, &r.Backend, &r.Provider, &r.Region, &r.RouteReason, &r.FallbackFrom,
		&r.InputTokens, &r.CachedInputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CostUSD,
		&r.Verdict, &r.InboundVerdict, &red, &rules, &r.Status, &r.ErrorCode, &r.ErrorDetail,
		&r.RequestHash, &r.ResponseHash, &r.ContentCaptured, &r.InFlight, &trace, &r.PolicyMode, &basis, &r.SecretID, &r.CacheWriteTokens, &r.OverheadUS, &r.ProjectID)
	if err != nil {
		return r, err
	}
	if basis != nil {
		r.CostBasis = new(model.CostBasis)
		if err := json.Unmarshal(basis, r.CostBasis); err != nil {
			return r, err
		}
	}
	r.TS = ts.UnixMilli()
	for _, p := range []struct {
		b []byte
		v any
	}{{red, &r.Redactions}, {rules, &r.Rules}, {trace, &r.Trace}} {
		if err := json.Unmarshal(p.b, p.v); err != nil {
			return r, err
		}
	}
	return r, nil
}

// Receipt fetches one receipt. ts narrows the chunk scan when known (0 = any).
func (s *Store) Receipt(ctx context.Context, tenant, id string, tsMS int64) (model.Receipt, error) {
	var row pgx.Row
	if tsMS > 0 {
		row = s.Receipts.QueryRow(ctx, selectReceipt+` WHERE tenant_id = $1 AND id = $2 AND ts = $3`, tenant, id, time.UnixMilli(tsMS))
	} else {
		row = s.Receipts.QueryRow(ctx, selectReceipt+` WHERE tenant_id = $1 AND id = $2 AND ts > now() - interval '30 days'`, tenant, id)
	}
	r, err := scanReceipt(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ReceiptQuery selects receipts for the Traffic list. Each filter matches any
// of its values; an empty filter matches everything. Model matches the
// requested or the resolved model, as the stream does.
type ReceiptQuery struct {
	Limit     int
	Before    int64 // epoch ms, exclusive; 0 = now
	Since     int64 // epoch ms, inclusive; 0 = the hot window (30 days)
	Keys      []string
	Teams     []string
	Projects  []string // by id
	Models    []string
	Verdicts  []string
	Providers []string
	Backends  []string
	Reasons   []string
	Sessions  []string
}

// Aggregable reports whether receipts_5m can count this query exactly: it
// keeps team, key, resolved model, backend and verdict, and nothing else.
// Model is left out because the list matches requested models too.
func (q ReceiptQuery) Aggregable() bool {
	return len(q.Projects)+len(q.Models)+len(q.Providers)+len(q.Reasons)+len(q.Sessions) == 0
}

// ListReceipts returns receipts matching q, newest first, for paging with Before.
func (s *Store) ListReceipts(ctx context.Context, tenant string, q ReceiptQuery) ([]model.Receipt, error) {
	before := time.Now().Add(time.Minute)
	if q.Before > 0 {
		before = time.UnixMilli(q.Before)
	}
	args := []any{tenant, before}
	where := `tenant_id = $1 AND ts < $2 AND ts > now() - interval '30 days'`
	if q.Since > 0 {
		args = append(args, time.UnixMilli(q.Since))
		where += fmt.Sprintf(` AND ts >= $%d`, len(args))
	}
	any := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		args = append(args, vals)
		where += fmt.Sprintf(` AND %s = ANY($%d)`, col, len(args))
	}
	any("key_id", q.Keys)
	any("team", q.Teams)
	any("project_id", q.Projects)
	any("verdict", q.Verdicts)
	any("provider", q.Providers)
	any("backend", q.Backends)
	any("route_reason", q.Reasons)
	any("session_id", q.Sessions)
	if len(q.Models) > 0 {
		args = append(args, q.Models)
		where += fmt.Sprintf(` AND (resolved_model = ANY($%[1]d) OR requested_model = ANY($%[1]d))`, len(args))
	}
	args = append(args, q.Limit)
	rows, _ := s.Receipts.Query(ctx, selectReceipt+` WHERE `+where+fmt.Sprintf(` ORDER BY ts DESC LIMIT $%d`, len(args)), args...)
	return collect(rows, func(r pgx.Rows) (model.Receipt, error) { return scanReceipt(r) })
}

// CountReceipts counts settled receipts matching q in [since, before) from
// receipts_5m (before 0 = now). Both must be on 5-minute boundaries for the
// count to be exact; the caller checks that and q.Aggregable().
func (s *Store) CountReceipts(ctx context.Context, tenant string, q ReceiptQuery) (int, error) {
	args := []any{tenant, time.UnixMilli(q.Since)}
	where := `tenant_id = $1 AND bucket >= $2`
	if q.Before > 0 {
		args = append(args, time.UnixMilli(q.Before))
		where += fmt.Sprintf(` AND bucket < $%d`, len(args))
	}
	any := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		args = append(args, vals)
		where += fmt.Sprintf(` AND %s = ANY($%d)`, col, len(args))
	}
	any("key_id", q.Keys)
	any("team", q.Teams)
	any("verdict", q.Verdicts)
	any("backend", q.Backends)
	var n int
	err := s.Receipts.QueryRow(ctx, `SELECT coalesce(sum(requests), 0)::int FROM receipts_5m WHERE `+where, args...).Scan(&n)
	return n, err
}

// TrafficSeries returns verdict counts per bucket, oldest first, with empty
// buckets filled so the chart x-axis stays regular.
func (s *Store) TrafficSeries(ctx context.Context, tenant string, bucket time.Duration, points int) ([]model.SeriesPoint, error) {
	end := time.Now().Truncate(bucket).Add(bucket)
	start := end.Add(-bucket * time.Duration(points))
	rows, _ := s.Receipts.Query(ctx, `
		SELECT time_bucket($2::interval, bucket) AS t, verdict, sum(requests)::int
		FROM receipts_5m WHERE tenant_id = $1 AND bucket >= $3
		GROUP BY 1, 2`, tenant, fmt.Sprintf("%d seconds", int(bucket.Seconds())), start)
	type row struct {
		t time.Time
		v string
		n int
	}
	got, err := collect(rows, func(r pgx.Rows) (row, error) {
		var x row
		return x, r.Scan(&x.t, &x.v, &x.n)
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.SeriesPoint, points)
	for i := range out {
		out[i].T = start.Add(bucket * time.Duration(i)).UnixMilli()
	}
	for _, x := range got {
		i := int(x.t.Sub(start) / bucket)
		if i < 0 || i >= points {
			continue
		}
		out[i].Add(x.v, x.n)
	}
	return out, nil
}

// SpendSeries returns daily spend by team for the last n UTC days, oldest first.
func (s *Store) SpendSeries(ctx context.Context, tenant string, days int, teams []string) ([]model.SpendPoint, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	start := today.AddDate(0, 0, -(days - 1))
	rows, _ := s.Receipts.Query(ctx, `
		SELECT bucket, team, coalesce(sum(cost_usd), 0)::float8 FROM receipts_daily
		WHERE tenant_id = $1 AND bucket >= $2 GROUP BY 1, 2`, tenant, start)
	type row struct {
		t    time.Time
		team string
		usd  float64
	}
	got, err := collect(rows, func(r pgx.Rows) (row, error) {
		var x row
		return x, r.Scan(&x.t, &x.team, &x.usd)
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.SpendPoint, days)
	for i := range out {
		out[i] = model.SpendPoint{Day: start.AddDate(0, 0, i).Format("01-02"), ByTeam: map[string]float64{}}
		for _, t := range teams {
			out[i].ByTeam[t] = 0
		}
	}
	for _, x := range got {
		i := int(x.t.UTC().Sub(start) / (24 * time.Hour))
		if i >= 0 && i < days {
			out[i].ByTeam[x.team] += x.usd
		}
	}
	return out, nil
}

// KeyUsage is a key's traffic over the rolling 24h: totals, and requests in
// 24 hourly bins (oldest first) that sum to Requests24h. Spend24hUSD leaves
// out the Unpriced24h requests, which have no price.
type KeyUsage struct {
	Requests24h int
	Spend24hUSD float64
	Unpriced24h int
	Hourly      [24]int
	LastUsed    *time.Time
}

// keyHour is the hourly bin, counted from the window start, that a
// receipts_5m bucket falls in. The bucket holding now joins the last bin.
func keyHour(from, bucket time.Time) int {
	return min(max(int(bucket.Sub(from)/time.Hour), 0), 23)
}

func (u *KeyUsage) add(from, bucket time.Time, requests int, costUSD float64) {
	u.Requests24h += requests
	u.Spend24hUSD += costUSD
	u.Hourly[keyHour(from, bucket)] += requests
}

func (s *Store) KeyUsage(ctx context.Context, tenant string) (map[string]KeyUsage, error) {
	out := map[string]KeyUsage{}
	from := time.Now().Add(-24 * time.Hour)
	rows, _ := s.Receipts.Query(ctx, `
		SELECT key_id, bucket, sum(requests)::int, coalesce(sum(cost_usd), 0)::float8 FROM receipts_5m
		WHERE tenant_id = $1 AND bucket > $2 GROUP BY 1, 2`, tenant, from)
	type cell struct {
		id     string
		bucket time.Time
		n      int
		cost   float64
	}
	cells, err := collect(rows, func(r pgx.Rows) (cell, error) {
		var c cell
		return c, r.Scan(&c.id, &c.bucket, &c.n, &c.cost)
	})
	if err != nil {
		return nil, err
	}
	for _, c := range cells {
		u := out[c.id]
		u.add(from, c.bucket, c.n, c.cost)
		out[c.id] = u
	}
	unpriced, err := s.UnpricedCells(ctx, tenant, from, time.Now(), 0)
	if err != nil {
		return nil, err
	}
	for _, c := range unpriced {
		u := out[c.KeyID]
		u.Unpriced24h += c.Requests
		out[c.KeyID] = u
	}
	rows, _ = s.Receipts.Query(ctx, `
		SELECT key_id, max(ts) FROM receipts
		WHERE tenant_id = $1 AND ts > now() - interval '30 days' GROUP BY 1`, tenant)
	last, err := collect(rows, func(r pgx.Rows) (struct {
		id string
		t  time.Time
	}, error) {
		var x struct {
			id string
			t  time.Time
		}
		return x, r.Scan(&x.id, &x.t)
	})
	if err != nil {
		return nil, err
	}
	for _, l := range last {
		u := out[l.id]
		t := l.t
		u.LastUsed = &t
		out[l.id] = u
	}
	return out, nil
}

// MonthSpend is month-to-date spend (UTC) keyed by team id and by key id.
type MonthSpend struct {
	ByTeam map[string]float64
	ByKey  map[string]float64
}

func (s *Store) MonthToDate(ctx context.Context, tenant string) (MonthSpend, error) {
	ms := MonthSpend{ByTeam: map[string]float64{}, ByKey: map[string]float64{}}
	rows, _ := s.Receipts.Query(ctx, `
		SELECT team, key_id, coalesce(sum(cost_usd), 0)::float8 FROM receipts_daily
		WHERE tenant_id = $1 AND bucket >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
		GROUP BY 1, 2`, tenant)
	defer rows.Close()
	for rows.Next() {
		var team, key string
		var usd float64
		if err := rows.Scan(&team, &key, &usd); err != nil {
			return ms, err
		}
		ms.ByTeam[team] += usd
		ms.ByKey[key] += usd
	}
	return ms, rows.Err()
}

type RuleCounts struct{ Last24h, Last7d int }

// RuleCounts counts matched rule evaluations. It reads raw receipts because
// continuous aggregates can't unnest jsonb arrays.
func (s *Store) RuleCounts(ctx context.Context, tenant string) (map[string]RuleCounts, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT e->>'ruleId',
		       count(*) FILTER (WHERE r.ts > now() - interval '24 hours')::int,
		       count(*)::int
		FROM receipts r, jsonb_array_elements(r.rules) e
		WHERE r.tenant_id = $1 AND r.ts > now() - interval '7 days' AND (e->>'matched')::boolean
		GROUP BY 1`, tenant)
	out := map[string]RuleCounts{}
	defer rows.Close()
	for rows.Next() {
		var id string
		var c RuleCounts
		if err := rows.Scan(&id, &c.Last24h, &c.Last7d); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// DetectorHits is what one entity detector did in the last 24 hours, as
// receipts record it: enforced redactions by type, and blocks whose detail
// names the entity. Monitor-mode matches record only "would redact", with no
// entity, so they aren't here.
type DetectorHits struct{ RedactedRequests, RedactedMatches, Blocked int }

func (s *Store) DetectorHits(ctx context.Context, tenant string) (map[string]DetectorHits, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT e->>'type', count(DISTINCT r.id)::int, coalesce(sum((e->>'count')::int), 0)::int, 0
		FROM receipts r, jsonb_array_elements(r.redactions) e
		WHERE r.tenant_id = $1 AND r.ts > now() - interval '24 hours'
		GROUP BY 1
		UNION ALL
		SELECT substring(error_detail from 'matched entity "([^"]+)"'), 0, 0, count(*)::int
		FROM receipts
		WHERE tenant_id = $1 AND ts > now() - interval '24 hours' AND error_code = 'policy_blocked' AND error_detail LIKE '%matched entity "%'
		GROUP BY 1`, tenant)
	out := map[string]DetectorHits{}
	defer rows.Close()
	for rows.Next() {
		var e string
		var h DetectorHits
		if err := rows.Scan(&e, &h.RedactedRequests, &h.RedactedMatches, &h.Blocked); err != nil {
			return nil, err
		}
		cur := out[e]
		out[e] = DetectorHits{cur.RedactedRequests + h.RedactedRequests, cur.RedactedMatches + h.RedactedMatches, cur.Blocked + h.Blocked}
	}
	return out, rows.Err()
}

type BackendStats struct {
	P50       int
	ErrorRate float64
	Requests  int
}

// BackendStats covers the last hour of settled traffic per backend. Errors
// are what the upstream failed, as RecentBackendFailures counts them.
func (s *Store) BackendStats(ctx context.Context, tenant string) (map[string]BackendStats, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT backend,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms)::int,
		       (100.0 * count(*) FILTER (WHERE error_code IN ('upstream_error', 'upstream_rate_limited')) / count(*))::float8,
		       count(*)::int
		FROM receipts
		WHERE tenant_id = $1 AND ts > now() - interval '1 hour' AND NOT in_flight AND status <> 403
		GROUP BY 1`, tenant)
	out := map[string]BackendStats{}
	defer rows.Close()
	for rows.Next() {
		var name string
		var b BackendStats
		if err := rows.Scan(&name, &b.P50, &b.ErrorRate, &b.Requests); err != nil {
			return nil, err
		}
		out[name] = b
	}
	return out, rows.Err()
}

// ReceiptAnyTenant fetches by id + ts only; the notify listener uses it.
func (s *Store) ReceiptAnyTenant(ctx context.Context, id string, tsMS int64) (model.Receipt, error) {
	r, err := scanReceipt(s.Receipts.QueryRow(ctx, selectReceipt+` WHERE id = $1 AND ts = $2`, id, time.UnixMilli(tsMS)))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}
