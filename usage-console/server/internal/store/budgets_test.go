package store

import (
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
)

func TestValidateBudget(t *testing.T) {
	scopes := BudgetScopes{
		Teams:    map[string]bool{"support": true},
		Keys:     map[string]bool{"support-bot": true},
		Projects: map[string]bool{"helpdesk": true},
	}
	ok := model.Budget{ScopeType: "project", Scope: "helpdesk", Period: "monthly", CapUSD: 500, OnExceed: "block"}
	for _, c := range []struct {
		name string
		edit func(*model.Budget)
		err  string
	}{
		{"valid", func(*model.Budget) {}, ""},
		{"team", func(b *model.Budget) { b.ScopeType, b.Scope = "team", "support" }, ""},
		{"key", func(b *model.Budget) { b.ScopeType, b.Scope = "key", "support-bot" }, ""},
		{"scope type", func(b *model.Budget) { b.ScopeType = "org" }, "scopeType must be team, project or key"},
		{"unknown team", func(b *model.Budget) { b.ScopeType, b.Scope = "team", "nobody" }, `no team "nobody"`},
		{"unknown key", func(b *model.Budget) { b.ScopeType, b.Scope = "key", "gone" }, `no active key named "gone"`},
		{"unknown project", func(b *model.Budget) { b.Scope = "nowhere" }, `no active key is in project "nowhere"`},
		{"period", func(b *model.Budget) { b.Period = "weekly" }, "only monthly budgets are enforced"},
		{"zero cap", func(b *model.Budget) { b.CapUSD = 0 }, "capUsd must be more than $0"},
		{"huge cap", func(b *model.Budget) { b.CapUSD = 1e13 }, "capUsd must be under $1,000,000,000,000"},
		{"sub-cent cap", func(b *model.Budget) { b.CapUSD = 10.005 }, "capUsd has at most 2 decimal places"},
		{"action", func(b *model.Budget) { b.OnExceed = "explode" }, "onExceed must be warn, throttle or block"},
	} {
		b := ok
		c.edit(&b)
		got := ""
		if err := ValidateBudget(b, scopes); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: got %q, want %q", c.name, got, c.err)
		}
	}
}

func TestBudgetChangeAuditWording(t *testing.T) {
	was := model.Budget{Scope: "agents", CapUSD: 32_000, OnExceed: "throttle"}
	for _, c := range []struct {
		name           string
		cap            float64
		onExceed       string
		action, target string
	}{
		{"raise", 40_000, "throttle", "Raised budget cap", "agents $32,000 → $40,000"},
		{"lower", 12_500.5, "throttle", "Lowered budget cap", "agents $32,000 → $12,500.50"},
		{"action", 32_000, "block", "Changed budget action", "agents throttle → block"},
		{"both", 40_000, "block", "Changed budget", "agents $32,000 → $40,000 · throttle → block"},
		{"nothing", 32_000, "throttle", "", ""},
	} {
		now := was
		now.CapUSD, now.OnExceed = c.cap, c.onExceed
		action, target := budgetChange(was, now)
		if action != c.action || target != c.target {
			t.Errorf("%s: got %q %q, want %q %q", c.name, action, target, c.action, c.target)
		}
	}
}
