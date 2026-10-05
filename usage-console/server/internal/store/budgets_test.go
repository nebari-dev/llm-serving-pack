package store

import (
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
)

func TestValidateBudget(t *testing.T) {
	// Keys and projects by id: a key budget names the key, not its name.
	scopes := BudgetScopes{
		Teams:    map[string]bool{"support": true},
		Keys:     map[string]bool{"k1": true},
		Projects: map[string]bool{"p1a2b3c4d": true},
	}
	ok := model.Budget{ScopeType: "project", Scope: "p1a2b3c4d", Period: "monthly", CapUSD: 500, OnExceed: "block"}
	for _, c := range []struct {
		name string
		edit func(*model.Budget)
		err  string
	}{
		{"valid", func(*model.Budget) {}, ""},
		{"team", func(b *model.Budget) { b.ScopeType, b.Scope = "team", "support" }, ""},
		{"key", func(b *model.Budget) { b.ScopeType, b.Scope = "key", "k1" }, ""},
		{"scope type", func(b *model.Budget) { b.ScopeType = "org" }, "scopeType must be team, project or key"},
		{"unknown team", func(b *model.Budget) { b.ScopeType, b.Scope = "team", "nobody" }, `no team "nobody"`},
		{"unknown key", func(b *model.Budget) { b.ScopeType, b.Scope = "key", "gone" }, `no active key with id "gone"`},
		{"key by name", func(b *model.Budget) { b.ScopeType, b.Scope = "key", "support-bot" }, `no active key with id "support-bot"`},
		{"unknown project", func(b *model.Budget) { b.Scope = "nowhere" }, `no project with id "nowhere"`},
		{"project by name", func(b *model.Budget) { b.Scope = "helpdesk" }, `no project with id "helpdesk"`},
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

func TestBudgetCovers(t *testing.T) {
	k := model.APIKey{ID: "k1", Name: "support-bot", Team: "support", Project: "helpdesk", ProjectID: "p1a2b3c4d"}
	for _, c := range []struct {
		scopeType, scope string
		want             bool
	}{
		{"team", "support", true},
		{"team", "web", false},
		{"key", "k1", true},
		{"key", "support-bot", false}, // names don't match: a rename or a reused name would
		{"project", "p1a2b3c4d", true},
		{"project", "helpdesk", false},  // nor do project names, which repeat across teams
		{"project", "p00000000", false}, // another team's helpdesk
	} {
		if got := BudgetCovers(model.Budget{ScopeType: c.scopeType, Scope: c.scope}, k); got != c.want {
			t.Errorf("%s %s: got %v", c.scopeType, c.scope, got)
		}
	}
}

func TestValidateProjectName(t *testing.T) {
	for name, want := range map[string]string{
		"helpdesk":              "",
		"nightly-digest_2":      "",
		"":                      "name is required",
		"Help Desk":             "name may use lowercase letters, digits, - and _, starting with a letter or digit",
		"-lead":                 "name may use lowercase letters, digits, - and _, starting with a letter or digit",
		strings.Repeat("a", 64): "name is at most 63 characters",
	} {
		got := ""
		if err := ValidateProjectName(name); err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}

// Audit targets name a key budget by the key's name, not its id.
func TestBudgetAuditTargetsUseTheScopeName(t *testing.T) {
	b := model.Budget{ScopeType: "key", Scope: "k3", ScopeName: "batch-summarize", CapUSD: 10, OnExceed: "warn"}
	now := b
	now.CapUSD = 20
	if _, target := budgetChange(b, now); target != "batch-summarize $10 → $20" {
		t.Errorf("change target %q", target)
	}
	if got := budgetTarget(b); got != "key batch-summarize · $10 monthly" {
		t.Errorf("target %q", got)
	}
	b.ScopeName = ""
	if got := budgetTarget(b); got != "key k3 · $10 monthly" {
		t.Errorf("target without a name %q", got)
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
