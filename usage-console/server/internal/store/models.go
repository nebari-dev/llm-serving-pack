package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

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
// alias first, then the trailing-* prefix pattern with the longest prefix.
func MatchAlias(aliases map[string]string, m string) (string, bool) {
	if _, ok := aliases[m]; ok {
		return m, true
	}
	best := ""
	for a := range aliases {
		if strings.HasSuffix(a, "*") && strings.HasPrefix(m, strings.TrimSuffix(a, "*")) && len(a) > len(best) {
			best = a
		}
	}
	return best, best != ""
}

// ValidateAlias checks an alias write against the catalog's model ids.
func ValidateAlias(alias, target string, catalog []string) error {
	prefix, pattern := strings.CutSuffix(alias, "*")
	switch {
	case alias == "":
		return errors.New("alias is required")
	case strings.ContainsFunc(alias, unicode.IsSpace):
		return errors.New("alias can't contain spaces")
	case strings.Contains(prefix, "*"):
		return errors.New("* may only end an alias")
	case pattern && prefix == "":
		return errors.New("a pattern needs a prefix before the *")
	case !slices.Contains(catalog, target):
		return fmt.Errorf("unknown target model %q", target)
	case alias == target:
		return errors.New("an alias can't point at itself")
	}
	if pattern {
		var captured []string
		for _, m := range catalog {
			if m != target && strings.HasPrefix(m, prefix) {
				captured = append(captured, m)
			}
		}
		if len(captured) > 0 {
			return fmt.Errorf("%s would also capture %s, which the catalog serves directly", alias, strings.Join(captured, ", "))
		}
	}
	return nil
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

// AliasRow is what an alias write can change; its ETag is the alias's version.
type AliasRow struct {
	Alias  string `json:"alias"`
	Target string `json:"target"`
}

// lockAlias reads an alias for update, or nil if there isn't one.
func lockAlias(ctx context.Context, tx pgx.Tx, tenant, alias string) (*AliasRow, error) {
	a := AliasRow{Alias: alias}
	err := tx.QueryRow(ctx, `SELECT target FROM model_aliases WHERE tenant_id = $1 AND alias = $2 FOR UPDATE`, tenant, alias).Scan(&a.Target)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// PutAlias creates an alias or points it at a new target. Setting the target
// it already has changes nothing and writes no audit row.
func (s *Store) PutAlias(ctx context.Context, tenant, actor, alias, target, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cur, err := lockAlias(ctx, tx, tenant, alias)
	if err != nil {
		return err
	}
	if cur == nil {
		if err := checkMatch(ifMatch, nil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_aliases (tenant_id, alias, target) VALUES ($1,$2,$3)`, tenant, alias, target); err != nil {
			return uniqueConflict(err)
		}
		if err := audit(ctx, tx, tenant, actor, "Created alias", alias+" → "+target, "Alias", alias, nil, AliasRow{alias, target}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err := checkMatch(ifMatch, *cur); err != nil {
		return err
	}
	if cur.Target == target {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE model_aliases SET target = $3 WHERE tenant_id = $1 AND alias = $2`, tenant, alias, target); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Changed alias target", alias+" "+cur.Target+" → "+target, "Alias", alias, *cur, AliasRow{alias, target}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteAlias(ctx context.Context, tenant, actor, alias, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cur, err := lockAlias(ctx, tx, tenant, alias)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrNotFound
	}
	if err := checkMatch(ifMatch, *cur); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM model_aliases WHERE tenant_id = $1 AND alias = $2`, tenant, alias); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Deleted alias", alias, "Alias", alias, *cur, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
