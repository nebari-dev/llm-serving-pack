package api

import (
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pricing"
	"github.com/jbouder/stargate/server/internal/store"
)

func rates(in, out float64) pricing.Rates {
	return pricing.Rates{&in, &in, &in, &out, &out}
}

func priceRow(m, b string, from time.Time, to *time.Time, r pricing.Rates) store.PriceRow {
	return store.PriceRow{ModelID: m, Backend: b, Rates: r, From: from, To: to}
}

var (
	savingsNow   = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	savingsFrom  = savingsNow.Add(-30 * 24 * time.Hour)
	savingsModel = map[string]model.Model{
		"gpt-5.5":          {ID: "gpt-5.5", Family: "gpt-5", Context: 400_000},
		"gpt-5-mini":       {ID: "gpt-5-mini", Family: "gpt-5", Context: 400_000},
		"claude-sonnet-5":  {ID: "claude-sonnet-5", Family: "claude", Context: 1_000_000},
		"claude-haiku-4-5": {ID: "claude-haiku-4-5", Family: "claude", Context: 200_000},
		"claude-opus-4-1":  {ID: "claude-opus-4-1", Family: "claude", Context: 200_000},
		"llama-3.3-70b":    {ID: "llama-3.3-70b", Family: "llama", Context: 128_000},
	}
)

func savingsGrouper() grouper {
	return grouper{
		models: savingsModel,
		keys: map[string]model.APIKey{
			"k1": {ID: "k1", Name: "support-bot", Status: "active", AllowedModels: []string{"claude-sonnet-5", "claude-haiku-4-5"}},
			"k2": {ID: "k2", Name: "agents-prod", Status: "active", AllowedModels: []string{"claude-sonnet-5", "gpt-5.5"}},
			"k3": {ID: "k3", Name: "batch", Status: "active", AllowedModels: []string{"gpt-5.5", "gpt-5-mini"}},
			"k9": {ID: "k9", Name: "old", Status: "revoked", AllowedModels: []string{"gpt-5.5"}},
		},
		backends: map[string]model.Backend{
			"openai-prod":   {Name: "openai-prod", Models: []string{"gpt-5.5", "gpt-5-mini"}},
			"anthropic-us":  {Name: "anthropic-us", Models: []string{"claude-sonnet-5", "claude-opus-4-1"}},
			"bedrock-us":    {Name: "bedrock-us", Models: []string{"claude-haiku-4-5"}},
			"vllm-internal": {Name: "vllm-internal", Models: []string{"llama-3.3-70b"}},
		},
	}
}

// cell is n requests of in input and out output tokens each, which cost usd
// in all (nil: unpriced).
func cell(key, requested, resolved string, period, n, in, out int, usd *float64) store.SavingsCell {
	c := store.SavingsCell{KeyID: key, Requested: requested, Resolved: resolved, Period: period, Requests: n,
		Short: out <= savingsOutputLimit, Priced: usd != nil, First: savingsFrom.Add(time.Hour)}
	c.Fit = fitBucket(contextThresholds(savingsModel), in+out)
	c.Tokens = pricing.Tokens{Input: n * in, Output: n * out}
	if usd != nil {
		c.USD = *usd
	}
	return c
}

func TestFitBucketMatchesTheCheapModelsContext(t *testing.T) {
	th := contextThresholds(savingsModel) // 128k, 200k, 400k, 1M
	for _, c := range []struct {
		size, ctx int
		fits      bool
	}{
		{200_000, 200_000, true}, // exactly the context fits
		{200_001, 200_000, false},
		{150_000, 200_000, true},
		{150_000, 128_000, false},
		{1_000_001, 1_000_000, false},
		{10, 128_000, true},
	} {
		if got := fitsContext(th, fitBucket(th, c.size), c.ctx); got != c.fits {
			t.Errorf("size %d in context %d: fits %v, want %v", c.size, c.ctx, got, c.fits)
		}
	}
}

func TestSavingsPricesTheCheaperSiblingAtEachRequestsOwnTime(t *testing.T) {
	change := savingsFrom.Add(10 * 24 * time.Hour)
	periods := []time.Time{savingsFrom, change}
	prices := map[[2]string][]store.PriceRow{
		{"gpt-5.5", "openai-prod"}:    {priceRow("gpt-5.5", "openai-prod", savingsFrom.Add(-time.Hour), nil, rates(10, 40))},
		{"gpt-5-mini", "openai-prod"}: {priceRow("gpt-5-mini", "openai-prod", savingsFrom.Add(-time.Hour), &change, rates(1, 4)), priceRow("gpt-5-mini", "openai-prod", change, nil, rates(2, 8))},
	}
	// 100 short gpt-5.5 requests in each period, 1,000 in and 100 out: $0.014 each at gpt-5.5's rates.
	in := savingsInput{
		Now: savingsNow, From: savingsFrom, Periods: periods, Fits: contextThresholds(savingsModel),
		G: savingsGrouper(), Prices: prices, Aliases: map[string]string{"summarize-*": "gpt-5.5"},
		Cells: []store.SavingsCell{
			cell("k3", "summarize-v2", "gpt-5.5", 1, 100, 1000, 100, usd(1.4)),
			cell("k3", "summarize-v2", "gpt-5.5", 2, 100, 1000, 100, usd(1.4)),
			// Long answers aren't counted.
			cell("k3", "summarize-v2", "gpt-5.5", 2, 7, 1000, 5000, usd(1.47)),
			// Nor unpriced ones, nor a revoked key's.
			cell("k3", "summarize-v2", "gpt-5.5", 2, 3, 1000, 100, nil),
			cell("k9", "summarize-v2", "gpt-5.5", 2, 5, 1000, 100, usd(0.07)),
		},
	}
	v := analyzeSavings(in)
	if len(v.Opportunities) != 1 {
		t.Fatalf("opportunities %+v", v.Opportunities)
	}
	o := v.Opportunities[0]
	if o.Alias != "summarize-*" || o.Model != "gpt-5.5" || o.Target != "gpt-5-mini" || o.TargetBackend != "openai-prod" {
		t.Fatalf("opportunity %+v", o)
	}
	// Before the change: 100 × (1000×1 + 100×4)/1M = $0.14; after: $0.28.
	if o.Requests != 200 || !near(o.ActualUSD, 2.8) || !near(o.TargetUSD, 0.42) || !near(o.SavedUSD, 2.38) {
		t.Errorf("requests %d actual %v target %v saved %v", o.Requests, o.ActualUSD, o.TargetUSD, o.SavedUSD)
	}
	if o.Served != 215 || o.Excluded.LongOutput != 7 || o.Excluded.Unpriced != 3 || o.Excluded.KeyInactive != 5 {
		t.Errorf("served %d excluded %+v", o.Served, o.Excluded)
	}
	if len(o.Keys) != 1 || o.Keys[0] != "batch" || len(o.NotAllowedKeys) != 0 {
		t.Errorf("keys %v not allowed %v", o.Keys, o.NotAllowedKeys)
	}
	if v.Served != 215 || v.Unpriced != 3 || v.OutputLimit != savingsOutputLimit || v.FirstAt != savingsFrom.Add(time.Hour).UnixMilli() {
		t.Errorf("view %+v", v)
	}
}

func TestSavingsPicksTheBestSiblingAndSaysWhichKeysDontAllowIt(t *testing.T) {
	prices := map[[2]string][]store.PriceRow{
		{"claude-sonnet-5", "anthropic-us"}: {priceRow("claude-sonnet-5", "anthropic-us", savingsFrom, nil, rates(3, 15))},
		{"claude-opus-4-1", "anthropic-us"}: {priceRow("claude-opus-4-1", "anthropic-us", savingsFrom, nil, rates(15, 75))},
		{"claude-haiku-4-5", "bedrock-us"}:  {priceRow("claude-haiku-4-5", "bedrock-us", savingsFrom, nil, rates(1, 5))},
	}
	in := savingsInput{
		Now: savingsNow, From: savingsFrom, Periods: []time.Time{savingsFrom}, Fits: contextThresholds(savingsModel),
		G: savingsGrouper(), Prices: prices, Aliases: map[string]string{},
		Cells: []store.SavingsCell{
			// Asked for by name: one opportunity per key.
			cell("k1", "claude-sonnet-5", "claude-sonnet-5", 1, 10, 2000, 50, usd(10*(2000*3+50*15)/1e6)),
			cell("k2", "claude-sonnet-5", "claude-sonnet-5", 1, 4, 2000, 50, usd(4*(2000*3+50*15)/1e6)),
			// Too big for haiku's 200k context.
			cell("k1", "claude-sonnet-5", "claude-sonnet-5", 1, 2, 300_000, 50, usd(2*(300_000*3+50*15)/1e6)),
			// A fallback or policy reroute: an alias change wouldn't move it.
			cell("k1", "claude-sonnet-5", "claude-opus-4-1", 1, 6, 2000, 50, usd(0.2)),
			// No cheaper sibling with a backend.
			cell("k1", "llama-3.3-70b", "llama-3.3-70b", 1, 6, 2000, 50, usd(0.01)),
		},
	}
	v := analyzeSavings(in)
	if len(v.Opportunities) != 2 {
		t.Fatalf("opportunities %+v", v.Opportunities)
	}
	a, b := v.Opportunities[0], v.Opportunities[1]
	if a.Key != "support-bot" || a.KeyID != "k1" || a.Alias != "" || a.Target != "claude-haiku-4-5" || a.TargetBackend != "bedrock-us" {
		t.Fatalf("first %+v", a)
	}
	if a.Requests != 10 || a.Excluded.OverContext != 2 || a.Served != 12 || !near(a.SavedUSD, 10*(2000*2+50*10)/1e6) {
		t.Errorf("first %+v", a)
	}
	// agents-prod doesn't allow haiku: still a saving, with what to change first.
	if b.Key != "agents-prod" || len(b.NotAllowedKeys) != 1 || b.NotAllowedKeys[0] != "agents-prod" || b.NotAllowedRequests != 4 {
		t.Errorf("second %+v", b)
	}
	if v.Rerouted != 6 {
		t.Errorf("rerouted %d", v.Rerouted)
	}
}

func TestSavingsNeverCountsATargetWithNoPriceAsFree(t *testing.T) {
	prices := map[[2]string][]store.PriceRow{
		{"gpt-5.5", "openai-prod"}: {priceRow("gpt-5.5", "openai-prod", savingsFrom, nil, rates(10, 40))},
		// gpt-5-mini has an input rate only: a request with output can't be priced.
		{"gpt-5-mini", "openai-prod"}: {priceRow("gpt-5-mini", "openai-prod", savingsFrom, nil, pricing.Rates{usd(1), usd(1), usd(1), nil, nil})},
	}
	in := savingsInput{
		Now: savingsNow, From: savingsFrom, Periods: []time.Time{savingsFrom}, Fits: contextThresholds(savingsModel),
		G: savingsGrouper(), Prices: prices, Aliases: map[string]string{},
		Cells: []store.SavingsCell{cell("k3", "gpt-5.5", "gpt-5.5", 1, 10, 1000, 100, usd(0.14))},
	}
	if v := analyzeSavings(in); len(v.Opportunities) != 0 {
		t.Fatalf("an unpriced target read as a saving: %+v", v.Opportunities)
	}
}

func TestSavingsSkipsTheAliasesOwnTargetAndPricierSiblings(t *testing.T) {
	prices := map[[2]string][]store.PriceRow{
		{"gpt-5-mini", "openai-prod"}: {priceRow("gpt-5-mini", "openai-prod", savingsFrom, nil, rates(1, 4))},
		{"gpt-5.5", "openai-prod"}:    {priceRow("gpt-5.5", "openai-prod", savingsFrom, nil, rates(10, 40))},
	}
	in := savingsInput{
		Now: savingsNow, From: savingsFrom, Periods: []time.Time{savingsFrom}, Fits: contextThresholds(savingsModel),
		G: savingsGrouper(), Prices: prices, Aliases: map[string]string{"cheap": "gpt-5-mini"},
		// Already on the cheapest: gpt-5.5 would cost more, so nothing to say.
		Cells: []store.SavingsCell{cell("k3", "cheap", "gpt-5-mini", 1, 10, 1000, 100, usd(0.014))},
	}
	if v := analyzeSavings(in); len(v.Opportunities) != 0 {
		t.Fatalf("opportunities %+v", v.Opportunities)
	}
}

func TestSavingsPeriodsSplitAtEveryPriceChange(t *testing.T) {
	a := savingsFrom.Add(24 * time.Hour)
	b := savingsFrom.Add(48 * time.Hour)
	rows := []store.PriceRow{
		priceRow("x", "y", savingsFrom.Add(-time.Hour), &a, pricing.Rates{}),
		priceRow("x", "y", a, &b, pricing.Rates{}),
		priceRow("x", "y", b, nil, pricing.Rates{}),
		priceRow("z", "y", savingsNow.Add(time.Hour), nil, pricing.Rates{}), // scheduled, after the window
	}
	got := savingsPeriods(savingsFrom, savingsNow, rows)
	if len(got) != 3 || !got[0].Equal(savingsFrom) || !got[1].Equal(a) || !got[2].Equal(b) {
		t.Fatalf("periods %v", got)
	}
}
