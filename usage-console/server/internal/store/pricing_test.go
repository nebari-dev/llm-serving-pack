package store

import (
	"testing"
	"time"
)

func TestValidatePrice(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	latest := now.Add(-30 * 24 * time.Hour)
	ok := PriceRow{ModelID: "gpt-5-mini", InPerM: 0.3, OutPerM: 2.4, CachedPerM: 0.03, ReasoningPerM: 2.4, From: now}
	for _, c := range []struct {
		name   string
		edit   func(*PriceRow)
		latest time.Time
		err    string
	}{
		{"now", func(*PriceRow) {}, latest, ""},
		{"scheduled", func(p *PriceRow) { p.From = now.Add(48 * time.Hour) }, latest, ""},
		{"free model", func(p *PriceRow) { p.InPerM, p.OutPerM, p.CachedPerM, p.ReasoningPerM = 0, 0, 0, 0 }, latest, ""},
		{"negative", func(p *PriceRow) { p.OutPerM = -1 }, latest, "rates can't be negative"},
		{"huge", func(p *PriceRow) { p.InPerM = 1e7 }, latest, "rates must be under $1,000,000 per million tokens"},
		{"fine grain", func(p *PriceRow) { p.InPerM = 0.1234567 }, latest, "rates have at most 6 decimal places"},
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
	was := PriceRow{ModelID: "gpt-5-mini", InPerM: 0.25, OutPerM: 2, CachedPerM: 0.025, ReasoningPerM: 2}
	now := was
	now.InPerM, now.OutPerM, now.From = 0.3, 2.4, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got, want := priceChange(was, now), "gpt-5-mini input $0.25 → $0.30, output $2 → $2.40 per 1M from 2026-10-01 00:00 UTC"; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := priceChange(was, was); got != "" {
		t.Errorf("same rates: got %q, want no change", got)
	}
}
