package api

import (
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func TestProjectionBasis(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	start, end := monthBounds(now)
	if !start.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("month %s – %s", start, end)
	}
	// $1,000 so far, $100/day, 5.5 days left.
	if got := project(1000, 100, end.Sub(now).Hours()/24); got != 1550 {
		t.Fatalf("projected %v, want 1550", got)
	}

	from, days := trailingWindow(now, time.Time{})
	if days != 7 || !from.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("full history: from %s days %v", from, days)
	}
	// Two and a half days of receipts: average over those, not over seven.
	first := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	if from, days = trailingWindow(now, first); days != 2.5 || !from.Equal(first) {
		t.Fatalf("short history: from %s days %v", from, days)
	}
}

func TestGrouperUsesTheCatalog(t *testing.T) {
	g := grouper{
		teams:    map[string]model.Team{"support": {ID: "support", Name: "Support", CostCenter: "CC-1"}},
		keys:     map[string]model.APIKey{"k1": {ID: "k1", Name: "support-bot", Team: "support", Project: "helpdesk"}},
		backends: map[string]model.Backend{"anthropic-us": {Name: "anthropic-us", Provider: "Anthropic"}},
		models:   map[string]model.Model{},
	}
	c := store.SpendCell{Team: "support", KeyID: "k1", Model: "claude-sonnet-5", Backend: "anthropic-us"}
	want := map[string]string{"team": "support", "project": "helpdesk", "key": "support-bot", "model": "claude-sonnet-5", "provider": "Anthropic"}
	for by, id := range want {
		if got := g.key(c, by); got != id {
			t.Errorf("key(%s) = %q, want %q", by, got, id)
		}
	}
	gone := store.SpendCell{Team: "support", KeyID: "k9", Backend: "old-backend"}
	if g.key(gone, "key") != "k9" || g.key(gone, "provider") != "old-backend" {
		t.Errorf("unknown key or backend should fall back to its id")
	}
	if label, sub := g.label("old-backend", "provider"); label != "old-backend" || sub == "" {
		t.Errorf("unconfigured backend label %q sub %q", label, sub)
	}
	anon := store.SpendCell{Model: "gpt-5-mini", Backend: "anthropic-us"}
	if g.key(anon, "team") != unattributed || g.key(anon, "project") != unattributed || g.key(anon, "provider") != "Anthropic" {
		t.Errorf("a receipt without a key should be unattributed by team, key and project only")
	}
	if label, sub := g.label("support", "team"); label != "Support" || sub != "CC-1" {
		t.Errorf("team label %q sub %q", label, sub)
	}
}
