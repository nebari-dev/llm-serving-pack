package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrBadOrder is a new order that isn't the current rules, each once.
type ErrBadOrder struct{ error }

// ErrSameOrder is a reorder that moves nothing.
var ErrSameOrder = errors.New("that is the order already in effect")

// planReorder checks a reorder against the current order (rule ids by
// ordinal): `from` is the order its author saw, `to` the one they want.
// It returns the moves, as the audit row's target.
func planReorder(cur, from, to []string, names map[string]string) (string, error) {
	if !slices.Equal(cur, from) {
		return "", ErrConflict
	}
	if len(to) != len(cur) {
		return "", ErrBadOrder{fmt.Errorf("the new order has %d rules; there are %d", len(to), len(cur))}
	}
	seen := map[string]bool{}
	for _, id := range to {
		if seen[id] || !slices.Contains(cur, id) {
			return "", ErrBadOrder{fmt.Errorf("the new order must list each current rule once (%s)", id)}
		}
		seen[id] = true
	}
	var moves []string
	for i, id := range to {
		if was := slices.Index(cur, id); was != i {
			moves = append(moves, fmt.Sprintf("%s %d → %d", names[id], was+1, i+1))
		}
	}
	if len(moves) == 0 {
		return "", ErrSameOrder
	}
	return strings.Join(moves, ", "), nil
}

// ReorderRules sets every rule's ordinal from `to`, if the order is still
// `from`, with an audit row.
func (s *Store) ReorderRules(ctx context.Context, tenant, actor string, from, to []string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, _ := tx.Query(ctx, `SELECT id, name FROM policy_rules WHERE tenant_id = $1 ORDER BY ordinal FOR UPDATE`, tenant)
	var cur []string
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		cur, names[id] = append(cur, id), name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	moves, err := planReorder(cur, from, to, names)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE policy_rules p SET ordinal = o.n FROM unnest($2::text[]) WITH ORDINALITY AS o(id, n) WHERE p.tenant_id = $1 AND p.id = o.id`, tenant, to); err != nil {
		return err
	}
	if err := audit(ctx, tx, tenant, actor, "Reordered rules", moves, "Policy", "", cur, to); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
