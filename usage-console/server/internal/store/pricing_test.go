package store

import (
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/pricing"
)

func per(v ...float64) (r pricing.Rates) {
	for i := range v {
		r[i] = &v[i]
	}
	return r
}

func set(r pricing.Rates, i pricing.Rate, v float64) pricing.Rates {
	r[i] = &v
	return r
}

func TestValidatePrice(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	latest := now.Add(-30 * 24 * time.Hour)
	ok := PriceRow{ModelID: "gpt-5-mini", Backend: "openai-prod", Rates: per(0.3, 0.03, 0.3, 2.4, 2.4), From: now}
	for _, c := range []struct {
		name   string
		edit   func(*PriceRow)
		latest time.Time
		err    string
	}{
		{"now", func(*PriceRow) {}, latest, ""},
		{"scheduled", func(p *PriceRow) { p.From = now.Add(48 * time.Hour) }, latest, ""},
		{"free model", func(p *PriceRow) { p.Rates = per(0, 0, 0, 0, 0) }, latest, ""},
		{"partly unpriced", func(p *PriceRow) { p.Rates[pricing.CacheWrite] = nil }, latest, ""},
		{"first price", func(*PriceRow) {}, time.Time{}, ""},
		{"negative", func(p *PriceRow) { p.Rates = set(p.Rates, pricing.Output, -1) }, latest, "rates can't be negative"},
		{"huge", func(p *PriceRow) { p.Rates = set(p.Rates, pricing.Input, 1e7) }, latest, "rates must be under $1,000,000 per million tokens"},
		{"fine grain", func(p *PriceRow) { p.Rates = set(p.Rates, pricing.Input, 0.1234567) }, latest, "rates have at most 6 decimal places"},
		{"backdated", func(p *PriceRow) { p.From = now.Add(-time.Hour) }, latest, "a price can't take effect in the past: receipts already costed keep their rate"},
		{"before a scheduled change", func(p *PriceRow) { p.From = now.Add(time.Hour) }, now.Add(24 * time.Hour), "a price already takes effect at 2026-09-30 12:00 UTC; cancel it first"},
	} {
		p := ok
		c.edit(&p)
		got := ""
		if err := ValidatePrice(p, c.latest, now); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: got %q, want %q", c.name, got, c.err)
		}
	}
}

func TestPriceChangeAuditWording(t *testing.T) {
	lite := pricing.Sources{pricing.LiteLLM, pricing.LiteLLM, pricing.LiteLLM, pricing.LiteLLM, pricing.LiteLLM}
	was := PriceRow{ModelID: "gpt-5-mini", Backend: "openai-prod", Rates: per(0.25, 0.025, 0.25, 2, 2), Sources: lite}
	now := was
	now.Rates = set(set(was.Rates, pricing.Input, 0.3), pricing.Output, 2.4)
	now.Sources[pricing.Input] = pricing.Manual
	now.From = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got, want := priceChange(was, now), "gpt-5-mini on openai-prod input $0.25 → $0.30, output $2 → $2.40 per 1M from 2026-10-01 00:00 UTC"; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := priceChange(was, was); got != "" {
		t.Errorf("same rates: got %q, want no change", got)
	}
	back := now
	back.Sources[pricing.Input] = pricing.LiteLLM
	if got, want := priceChange(now, back), "gpt-5-mini on openai-prod input $0.30 now follows LiteLLM per 1M from 2026-10-01 00:00 UTC"; got != want {
		t.Errorf("source only: got %q\nwant %q", got, want)
	}
	gone := was
	gone.Rates, gone.Sources = pricing.Rates{}, pricing.Sources{}
	if got, want := priceChange(was, gone), "gpt-5-mini on openai-prod input $0.25 → no price, cached input $0.025 → no price, cache write $0.25 → no price, output $2 → no price, reasoning $2 → no price per 1M from 0001-01-01 00:00 UTC"; got != want {
		t.Errorf("retired: got %q\nwant %q", got, want)
	}
}
