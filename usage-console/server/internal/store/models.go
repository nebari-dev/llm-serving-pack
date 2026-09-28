package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ResolveAlias maps what a client asked for to a catalog model.
func ResolveAlias(aliases map[string]string, m string) (string, bool) {
	if a, ok := MatchAlias(aliases, m); ok {
		return aliases[a], true
	}
	return m, false
}

// MatchAlias is the alias a requested model resolves through: an exact
// alias first, then a trailing-* prefix pattern.
func MatchAlias(aliases map[string]string, m string) (string, bool) {
	if _, ok := aliases[m]; ok {
		return m, true
	}
	for a := range aliases {
		if strings.HasSuffix(a, "*") && strings.HasPrefix(m, strings.TrimSuffix(a, "*")) {
			return a, true
		}
	}
	return "", false
}

// PriceRow is one effective-dated model_pricing row.
type PriceRow struct {
	ModelID                                    string
	InPerM, OutPerM, CachedPerM, ReasoningPerM float64
	From                                       time.Time
	To                                         *time.Time
}

// PriceRows returns every pricing row, per model oldest first.
func (s *Store) PriceRows(ctx context.Context) ([]PriceRow, error) {
	rows, _ := s.Config.Query(ctx, `
		SELECT model_id, in_per_m::float8, out_per_m::float8, cached_per_m::float8, reasoning_per_m::float8, effective_from, effective_to
		FROM model_pricing ORDER BY model_id, effective_from`)
	return collect(rows, func(r pgx.Rows) (PriceRow, error) {
		var p PriceRow
		return p, r.Scan(&p.ModelID, &p.InPerM, &p.OutPerM, &p.CachedPerM, &p.ReasoningPerM, &p.From, &p.To)
	})
}

// RequestedModels counts the tenant's receipts since a time by what the
// client asked for. Raw receipts, because the aggregates only keep the
// resolved model and route_reason is overwritten by policy and fallback.
func (s *Store) RequestedModels(ctx context.Context, tenant string, since time.Time) (map[string]int, error) {
	rows, _ := s.Receipts.Query(ctx, `
		SELECT requested_model, count(*)::int FROM receipts WHERE tenant_id = $1 AND ts > $2 GROUP BY 1`, tenant, since)
	type row struct {
		m string
		n int
	}
	got, err := collect(rows, func(r pgx.Rows) (row, error) {
		var x row
		return x, r.Scan(&x.m, &x.n)
	})
	out := map[string]int{}
	for _, x := range got {
		out[x.m] = x.n
	}
	return out, err
}
