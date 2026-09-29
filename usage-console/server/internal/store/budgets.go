package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jbouder/stargate/server/internal/model"
)

// BudgetScopes is what a budget may cover: team ids, and the names and
// projects of active keys.
type BudgetScopes struct {
	Teams, Keys, Projects map[string]bool
}

func (s *Store) BudgetScopes(ctx context.Context, tenant string) (BudgetScopes, error) {
	out := BudgetScopes{Teams: map[string]bool{}, Keys: map[string]bool{}, Projects: map[string]bool{}}
	teams, err := s.Teams(ctx, tenant)
	if err != nil {
		return out, err
	}
	for _, t := range teams {
		out.Teams[t.ID] = true
	}
	keys, err := s.Keys(ctx, tenant)
	if err != nil {
		return out, err
	}
	for _, k := range keys {
		if k.Status != "revoked" {
			out.Keys[k.Name], out.Projects[k.Project] = true, true
		}
	}
	return out, nil
}

// ValidateBudget checks a budget write. Spend is month to date, so monthly
// is the only period the gateway can enforce.
func ValidateBudget(b model.Budget, scopes BudgetScopes) error {
	switch b.ScopeType {
	case "team":
		if !scopes.Teams[b.Scope] {
			return fmt.Errorf("no team %q", b.Scope)
		}
	case "key":
		if !scopes.Keys[b.Scope] {
			return fmt.Errorf("no active key named %q", b.Scope)
		}
	case "project":
		if !scopes.Projects[b.Scope] {
			return fmt.Errorf("no active key is in project %q", b.Scope)
		}
	default:
		return errors.New("scopeType must be team, project or key")
	}
	switch {
	case b.Period != "monthly":
		return errors.New("only monthly budgets are enforced")
	case !(b.CapUSD > 0):
		return errors.New("capUsd must be more than $0")
	case b.CapUSD >= 1e12: // numeric(14,2)
		return errors.New("capUsd must be under $1,000,000,000,000")
	case math.Abs(b.CapUSD*100-math.Round(b.CapUSD*100)) > 1e-6:
		return errors.New("capUsd has at most 2 decimal places")
	case b.OnExceed != "warn" && b.OnExceed != "throttle" && b.OnExceed != "block":
		return errors.New("onExceed must be warn, throttle or block")
	}
	return nil
}

// BudgetETag is a budget's version for If-Match: the fields a write can change.
func BudgetETag(b model.Budget) string {
	return ETag([]any{b.ID, b.ScopeType, b.Scope, b.Period, b.CapUSD, b.OnExceed})
}

// usd is an audit-log amount: "$32,000", or "$12,500.50" with cents.
func usd(f float64) string {
	cents := int64(math.Round(f * 100))
	whole := fmt.Sprint(cents / 100)
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if cents%100 != 0 {
		return fmt.Sprintf("$%s.%02d", whole, cents%100)
	}
	return "$" + whole
}

// budgetChange is the audit action and target for an edit, or "" when the
// edit changes nothing.
func budgetChange(was, now model.Budget) (action, target string) {
	var parts []string
	capChanged, actionChanged := was.CapUSD != now.CapUSD, was.OnExceed != now.OnExceed
	if capChanged {
		parts = append(parts, usd(was.CapUSD)+" → "+usd(now.CapUSD))
	}
	if actionChanged {
		parts = append(parts, was.OnExceed+" → "+now.OnExceed)
	}
	switch {
	case capChanged && actionChanged:
		action = "Changed budget"
	case capChanged && now.CapUSD > was.CapUSD:
		action = "Raised budget cap"
	case capChanged:
		action = "Lowered budget cap"
	case actionChanged:
		action = "Changed budget action"
	default:
		return "", ""
	}
	return action, now.Scope + " " + strings.Join(parts, " · ")
}

const budgetCols = `id, scope, scope_type, period, cap_usd::float8, on_exceed`

func scanBudget(r pgx.Row) (model.Budget, error) {
	var b model.Budget
	err := r.Scan(&b.ID, &b.Scope, &b.ScopeType, &b.Period, &b.CapUSD, &b.OnExceed)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func newBudgetID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "b" + hex.EncodeToString(b)
}

func (s *Store) CreateBudget(ctx context.Context, tenant, actor string, b model.Budget) (model.Budget, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return b, err
	}
	defer tx.Rollback(ctx)
	b, err = scanBudget(tx.QueryRow(ctx, `
		INSERT INTO budgets (id, tenant_id, scope_type, scope, period, cap_usd, on_exceed) VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING `+budgetCols, newBudgetID(), tenant, b.ScopeType, b.Scope, b.Period, b.CapUSD, b.OnExceed))
	if err != nil {
		return b, uniqueConflict(err)
	}
	target := fmt.Sprintf("%s %s · %s monthly, %s", b.ScopeType, b.Scope, usd(b.CapUSD), b.OnExceed)
	if err := audit(ctx, tx, tenant, actor, "Created budget", target, "Budget", b.ID, nil, b); err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}

// UpdateBudget changes a budget's cap and action; its scope is fixed. An
// edit that changes nothing writes no audit row.
func (s *Store) UpdateBudget(ctx context.Context, tenant, actor, id, ifMatch string, edit func(*model.Budget)) (model.Budget, error) {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return model.Budget{}, err
	}
	defer tx.Rollback(ctx)
	was, err := scanBudget(tx.QueryRow(ctx, `SELECT `+budgetCols+` FROM budgets WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenant, id))
	if err != nil {
		return was, err
	}
	if ifMatch != "" && ifMatch != BudgetETag(was) {
		return was, &StaleError{Current: withVersion(was)}
	}
	now := was
	edit(&now)
	action, target := budgetChange(was, now)
	if action == "" {
		return was, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE budgets SET cap_usd = $3, on_exceed = $4 WHERE tenant_id = $1 AND id = $2`, tenant, id, now.CapUSD, now.OnExceed); err != nil {
		return was, err
	}
	if err := audit(ctx, tx, tenant, actor, action, target, "Budget", id, was, now); err != nil {
		return was, err
	}
	return now, tx.Commit(ctx)
}

// DeleteBudget removes a budget. Keys that named it keep working, with their
// budget_id cleared; enforcement goes by scope, not budget_id.
func (s *Store) DeleteBudget(ctx context.Context, tenant, actor, id, ifMatch string) error {
	tx, err := s.Config.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	was, err := scanBudget(tx.QueryRow(ctx, `SELECT `+budgetCols+` FROM budgets WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenant, id))
	if err != nil {
		return err
	}
	if ifMatch != "" && ifMatch != BudgetETag(was) {
		return &StaleError{Current: withVersion(was)}
	}
	if _, err := tx.Exec(ctx, `UPDATE api_keys SET budget_id = NULL WHERE tenant_id = $1 AND budget_id = $2`, tenant, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM budgets WHERE tenant_id = $1 AND id = $2`, tenant, id); err != nil {
		return err
	}
	target := fmt.Sprintf("%s %s · %s monthly", was.ScopeType, was.Scope, usd(was.CapUSD))
	if err := audit(ctx, tx, tenant, actor, "Deleted budget", target, "Budget", id, was, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func withVersion(b model.Budget) model.Budget {
	b.ETag = BudgetETag(b)
	return b
}

// BudgetCovers is whether a budget applies to a key: its team, its project,
// or the key itself. The gateway enforces every budget that covers a key.
func BudgetCovers(b model.Budget, k model.APIKey) bool {
	switch b.ScopeType {
	case "team":
		return b.Scope == k.Team
	case "project":
		return b.Scope == k.Project
	case "key":
		return b.Scope == k.Name
	}
	return false
}
