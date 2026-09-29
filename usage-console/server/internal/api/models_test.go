package api

import (
	"reflect"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func TestAliasViewsCountRequestsByWhatTheClientAskedFor(t *testing.T) {
	aliases := map[string]string{"summarize-*": "gpt-5-mini", "summarize-eu": "llama-3.3-70b", "fast": "claude-haiku-4-5"}
	requested := map[string]int{"summarize-digest": 887, "summarize-notes": 32, "summarize-eu": 5, "gpt-5.5": 1602}
	got := aliasViews(aliases, requested)
	for i := range got {
		if got[i].ETag != store.ETag(store.AliasRow{Alias: got[i].Alias, Target: got[i].Target}) {
			t.Errorf("%s: etag %s isn't its row's ETag", got[i].Alias, got[i].ETag)
		}
		got[i].ETag = ""
	}
	want := []model.Alias{
		{Alias: "fast", Target: "claude-haiku-4-5", Requests24h: 0},
		{Alias: "summarize-*", Target: "gpt-5-mini", Requests24h: 919},
		{Alias: "summarize-eu", Target: "llama-3.3-70b", Requests24h: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func day(s string) time.Time {
	d, _ := time.Parse(time.DateOnly, s)
	return d
}

func TestPricingViewDiffsConsecutiveRows(t *testing.T) {
	aug, sep := day("2026-08-14"), day("2026-09-01")
	rows := []store.PriceRow{
		{ModelID: "claude-sonnet-5", InPerM: 3, OutPerM: 18, CachedPerM: 0.3, ReasoningPerM: 18, From: day("2026-01-01"), To: &sep},
		{ModelID: "claude-sonnet-5", InPerM: 3, OutPerM: 15, CachedPerM: 0.3, ReasoningPerM: 15, From: sep},
		{ModelID: "gpt-5-mini", InPerM: 0.25, OutPerM: 2, CachedPerM: 0.05, From: day("2026-01-01"), To: &aug},
		{ModelID: "gpt-5-mini", InPerM: 0.25, OutPerM: 2, CachedPerM: 0.025, From: aug},
		{ModelID: "llama-3.3-70b", InPerM: 0.6, OutPerM: 0.6, From: day("2026-01-01")},
	}
	got := pricingView(rows, day("2026-09-28"))
	want := model.Pricing{
		EffectiveFrom: map[string]string{"claude-sonnet-5": "2026-09-01", "gpt-5-mini": "2026-08-14", "llama-3.3-70b": "2026-01-01"},
		Changes: []model.PriceChange{
			{Model: "claude-sonnet-5", Field: "Output", From: 18, To: 15, Effective: "2026-09-01"},
			{Model: "claude-sonnet-5", Field: "Reasoning", From: 18, To: 15, Effective: "2026-09-01"},
			{Model: "gpt-5-mini", Field: "Cached input", From: 0.05, To: 0.025, Effective: "2026-08-14"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestPricingViewWithOnlySeedRowsHasNoChanges(t *testing.T) {
	later := day("2026-12-01")
	rows := []store.PriceRow{
		{ModelID: "gpt-5.5", InPerM: 1.25, OutPerM: 10, From: day("2026-01-01"), To: &later},
		{ModelID: "gpt-5.5", InPerM: 1.25, OutPerM: 8, From: later}, // scheduled, not yet in force
	}
	got := pricingView(rows, day("2026-09-28"))
	if got.EffectiveFrom["gpt-5.5"] != "2026-01-01" {
		t.Fatalf("the row in force now is the January one: %+v", got.EffectiveFrom)
	}
	if len(got.Changes) != 1 || got.Changes[0].Effective != "2026-12-01" {
		t.Fatalf("a scheduled change is listed with its date: %+v", got.Changes)
	}
	empty := pricingView(rows[:1], day("2026-09-28"))
	if empty.Changes == nil || len(empty.Changes) != 0 {
		t.Fatalf("no history encodes as [], not null: %#v", empty.Changes)
	}
}
