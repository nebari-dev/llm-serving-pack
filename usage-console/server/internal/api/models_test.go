package api

import (
	"cmp"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pricing"
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

func per(v ...float64) (r pricing.Rates) {
	for i := range v {
		r[i] = &v[i]
	}
	return r
}

func srcs(s ...pricing.Source) (out pricing.Sources) {
	copy(out[:], s)
	return out
}

func TestPricingViewListsEveryPairAndDiffsItsRows(t *testing.T) {
	jan, sep, oct := day("2026-01-01"), day("2026-09-01"), day("2026-10-01")
	seed, lite, man := pricing.Seed, pricing.LiteLLM, pricing.Manual
	rows := []store.PriceRow{
		{ModelID: "claude-sonnet-5", Backend: "bedrock-eu", Rates: per(3, 0.3, 3, 15, 15), Sources: srcs(seed, seed, seed, seed, seed), From: jan, To: &sep},
		{ModelID: "claude-sonnet-5", Backend: "bedrock-eu", Rates: per(2.2, 0.22, 2.75, 11, 11), Sources: srcs(man, lite, lite, lite, lite), From: sep, To: &oct},
		{ModelID: "claude-sonnet-5", Backend: "bedrock-eu", Rates: per(2.2, 0.22, 2.75, 12, 12), Sources: srcs(man, lite, lite, lite, lite), From: oct}, // scheduled
		{ModelID: "llama-3.3-70b", Backend: "vllm-internal", Rates: per(0.12, 0.12, 0.12, 0.3, 0.3), Sources: srcs(seed, seed, seed, seed, seed), From: jan, To: &sep},
	}
	pairs := [][2]string{{"claude-sonnet-5", "bedrock-eu"}, {"llama-3.3-70b", "vllm-internal"}, {"claude-opus-4-1", "anthropic-prod"}}
	keys := map[[2]string]string{{"claude-sonnet-5", "bedrock-eu"}: "eu.anthropic.claude-sonnet-5"}
	got := pricingView(pairs, rows, keys, day("2026-09-28"))

	sonnet := got.Prices[0]
	if sonnet.Model != "claude-sonnet-5" || sonnet.Backend != "bedrock-eu" || sonnet.LiteLLMKey != "eu.anthropic.claude-sonnet-5" ||
		sonnet.EffectiveFrom != "2026-09-01" || !sonnet.Priced || sonnet.ETag != store.ETag(store.PriceVersion(rows[2])) {
		t.Fatalf("sonnet: %+v", sonnet)
	}
	if r := sonnet.Rates["input"]; r == nil || r.PerM != 2.2 || r.Source != "manual" {
		t.Fatalf("input is the override in effect now, not the scheduled row: %+v", r)
	}
	if r := sonnet.Rates["output"]; r == nil || r.PerM != 11 || r.Source != "litellm" {
		t.Fatalf("output: %+v", r)
	}
	llama := got.Prices[1]
	if llama.Priced || llama.EffectiveFrom != "" || llama.Rates["input"] != nil || llama.ETag != store.ETag(store.PriceVersion(rows[3])) {
		t.Fatalf("a retired price is no price: %+v", llama)
	}
	opus := got.Prices[2]
	if opus.Priced || opus.LiteLLMKey != "" || len(opus.Rates) != 0 || opus.ETag != store.ETag([2]string{"claude-opus-4-1", "anthropic-prod"}) {
		t.Fatalf("a pair that never had a price: %+v", opus)
	}

	f := func(v float64) *float64 { return &v }
	want := []model.PriceChange{
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Field: "Output", From: f(11), To: f(12), Source: "litellm", Effective: "2026-10-01", EffectiveAt: oct.UnixMilli(), Scheduled: true},
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Field: "Reasoning", From: f(11), To: f(12), Source: "litellm", Effective: "2026-10-01", EffectiveAt: oct.UnixMilli(), Scheduled: true},
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Field: "Input", From: f(3), To: f(2.2), Source: "manual", Effective: "2026-09-01", EffectiveAt: sep.UnixMilli()},
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Field: "Cached input", From: f(0.3), To: f(0.22), Source: "litellm", Effective: "2026-09-01", EffectiveAt: sep.UnixMilli()},
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Field: "Cache write", From: f(3), To: f(2.75), Source: "litellm", Effective: "2026-09-01", EffectiveAt: sep.UnixMilli()},
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Field: "Output", From: f(15), To: f(11), Source: "litellm", Effective: "2026-09-01", EffectiveAt: sep.UnixMilli()},
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Field: "Reasoning", From: f(15), To: f(11), Source: "litellm", Effective: "2026-09-01", EffectiveAt: sep.UnixMilli()},
	}
	for _, field := range []string{"Input", "Cached input", "Cache write", "Output", "Reasoning"} {
		want = append(want, model.PriceChange{Model: "llama-3.3-70b", Backend: "vllm-internal", Field: field, From: rows[3].Rates[slices.Index(pricing.Labels[:], field)], Effective: "2026-09-01", EffectiveAt: sep.UnixMilli()})
	}
	slices.SortStableFunc(want, func(x, y model.PriceChange) int { return cmp.Compare(y.EffectiveAt, x.EffectiveAt) })
	if !reflect.DeepEqual(got.Changes, want) {
		t.Fatalf("changes:\n got %s\nwant %s", show(got.Changes), show(want))
	}
}

func show(cs []model.PriceChange) string {
	b, _ := json.MarshalIndent(cs, "", " ")
	return string(b)
}

func TestPricingViewWithOnlySeedRowsHasNoChanges(t *testing.T) {
	rows := []store.PriceRow{{ModelID: "gpt-5.5", Backend: "openai-prod", Rates: per(1.25, 0.125, 1.25, 10, 10), From: day("2026-01-01")}}
	empty := pricingView([][2]string{{"gpt-5.5", "openai-prod"}}, rows, nil, day("2026-09-28"))
	if empty.Changes == nil || len(empty.Changes) != 0 {
		t.Fatalf("no history encodes as [], not null: %#v", empty.Changes)
	}
}

// There's no reconciler (§4.4): the sync_state column holds seed values
// nothing observed, so the API reports every backend and route as not
// reconciled rather than pass them on.
func TestSyncStateIsNotReconciled(t *testing.T) {
	bs := notReconciledBackends([]model.Backend{{Name: "a", Sync: "synced"}, {Name: "b", Sync: "drift"}})
	rs := notReconciledRoutes([]model.Route{{Name: "r", Sync: "applying"}})
	for _, s := range []string{bs[0].Sync, bs[1].Sync, rs[0].Sync} {
		if s != NotReconciled {
			t.Fatalf("sync %q", s)
		}
	}
}
