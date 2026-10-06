package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// BudgetDryRun is what a budget write would do, without doing it (§6
// dryRun): the budget with its spend and projection, the active keys it
// would cover, and whether it's over its cap now.
type BudgetDryRun struct {
	DryRun  bool         `json:"dryRun"`
	Budget  model.Budget `json:"budget"`
	Covers  []string     `json:"covers"`
	OverCap bool         `json:"overCap"`
}

func dryRun(r *http.Request) bool { return r.URL.Query().Get("dryRun") == "true" }

// createBudget takes {scopeType, scope, capUsd, onExceed, period?}.
func (s *Server) createBudget(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in model.Budget
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	b := model.Budget{ScopeType: in.ScopeType, Scope: in.Scope, Period: in.Period, CapUSD: in.CapUSD, OnExceed: in.OnExceed}
	if b.Period == "" {
		b.Period = "monthly"
	}
	if err := s.validBudget(r.Context(), t, b); err != nil {
		return nil, err
	}
	if dryRun(r) {
		return s.budgetDryRun(r.Context(), t, b)
	}
	b, err := s.Store.CreateBudget(r.Context(), t, actor(r), b)
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return s.budgetView(w, r.Context(), t, b)
}

// updateBudget takes {capUsd?, onExceed?}. The scope and period are fixed:
// a budget on another scope is a new budget.
func (s *Server) updateBudget(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	match := r.Header.Get("If-Match")
	if !dryRun(r) {
		var err error
		if match, err = ifMatch(r); err != nil {
			return nil, err
		}
	}
	var in struct {
		CapUSD   *float64 `json:"capUsd"`
		OnExceed *string  `json:"onExceed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	edit := func(b *model.Budget) {
		if in.CapUSD != nil {
			b.CapUSD = *in.CapUSD
		}
		if in.OnExceed != nil {
			b.OnExceed = *in.OnExceed
		}
	}
	id := r.PathValue("id")
	bs, err := s.Store.Budgets(r.Context(), t)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(bs, func(b model.Budget) bool { return b.ID == id })
	if i < 0 {
		return nil, store.ErrNotFound
	}
	next := bs[i]
	edit(&next)
	if err := s.validBudget(r.Context(), t, next); err != nil {
		return nil, err
	}
	if dryRun(r) {
		if match != "" && match != store.BudgetETag(bs[i]) {
			bs[i].ETag = store.BudgetETag(bs[i])
			return nil, &store.StaleError{Current: bs[i]}
		}
		return s.budgetDryRun(r.Context(), t, next)
	}
	b, err := s.Store.UpdateBudget(r.Context(), t, actor(r), id, match, edit)
	if err != nil {
		return nil, err
	}
	s.configChanged()
	return s.budgetView(w, r.Context(), t, b)
}

func (s *Server) deleteBudget(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	m, err := ifMatch(r)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	if err := s.Store.DeleteBudget(r.Context(), t, actor(r), id, m); err != nil {
		return nil, err
	}
	s.configChanged()
	return map[string]string{"id": id}, nil
}

// validBudget checks the budget's values, and that its scope is one it's
// allowed to name (a key's own scope may lose its key later: that's fine).
func (s *Server) validBudget(ctx context.Context, t string, b model.Budget) error {
	scopes, err := s.Store.BudgetScopes(ctx, t)
	if err != nil {
		return err
	}
	if err := store.ValidateBudget(b, scopes); err != nil {
		return badRequest(err.Error())
	}
	return nil
}

func (s *Server) budgetView(w http.ResponseWriter, ctx context.Context, t string, b model.Budget) (model.Budget, error) {
	vs, err := s.budgetViews(ctx, t, []model.Budget{b})
	if err != nil {
		return b, err
	}
	w.Header().Set("ETag", vs[0].ETag)
	return vs[0], nil
}

func (s *Server) budgetDryRun(ctx context.Context, t string, b model.Budget) (BudgetDryRun, error) {
	vs, err := s.budgetViews(ctx, t, []model.Budget{b})
	if err != nil {
		return BudgetDryRun{}, err
	}
	keys, err := s.Store.Keys(ctx, t)
	if err != nil {
		return BudgetDryRun{}, err
	}
	out := BudgetDryRun{DryRun: true, Budget: vs[0], Covers: []string{}, OverCap: vs[0].CurrentUSD >= b.CapUSD}
	out.Budget.ETag = ""
	for _, k := range keys {
		if k.Status != "revoked" && store.BudgetCovers(b, k.APIKey) {
			out.Covers = append(out.Covers, k.Name)
		}
	}
	slices.Sort(out.Covers)
	return out, nil
}
