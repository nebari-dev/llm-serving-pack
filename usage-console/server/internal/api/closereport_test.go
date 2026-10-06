package api

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func TestParseCloseMonth(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{
		"2026-09": "2026-09-01",
		"2026-10": "2026-10-01", // the open month, to date
		"2025-12": "2025-12-01",
	} {
		got, err := parseCloseMonth(in, now)
		if err != nil || got.Format(time.DateOnly) != want {
			t.Errorf("parseCloseMonth(%q) = %s, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "2026-11", "2026-13", "2026-9", "09-2026", "2026-09-01"} {
		if _, err := parseCloseMonth(in, now); err == nil {
			t.Errorf("parseCloseMonth(%q) accepted", in)
		}
	}
}

func closeGrouper() grouper {
	return grouper{
		teams:    map[string]model.Team{"support": {ID: "support", Name: "Support", CostCenter: "CC-100"}, "batch": {ID: "batch", Name: "Batch"}},
		keys:     map[string]model.APIKey{"k1": {ID: "k1", Name: "support-bot", Team: "support", ProjectID: "p1"}, "k3": {ID: "k3", Name: "nightly", Team: "batch", ProjectID: "p3"}},
		projects: map[string]model.Project{"p1": {ID: "p1", Team: "support", Name: "helpdesk"}, "p3": {ID: "p3", Team: "batch", Name: "digest"}},
		backends: map[string]model.Backend{"openai-prod": {Name: "openai-prod", Provider: "OpenAI"}, "vllm-internal": {Name: "vllm-internal", Provider: "Self-hosted"}},
		models:   map[string]model.Model{},
	}
}

func closeInputFor(month time.Time, now time.Time) closeInput {
	in5 := 1.25
	return closeInput{
		Tenant: "demo", Actor: "dev@stargate.local", Month: month, Now: now,
		RawFrom: now.Add(-30 * 24 * time.Hour),
		G:       closeGrouper(),
		Cells: []store.SpendCell{
			{Team: "support", KeyID: "k1", Model: "gpt-5-mini", Backend: "openai-prod", USD: 1234.5, Requests: 1000, Tokens: 900_000},
			{Team: "batch", KeyID: "k3", Model: "gpt-5-mini", Backend: "openai-prod", USD: 100.25, Requests: 200, Tokens: 50_000},
			{Team: "batch", KeyID: "k3", Model: "llama-3.3-70b", Backend: "vllm-internal", USD: 0, Requests: 40, Tokens: 4_000},
		},
		Unpriced: []store.UnpricedCell{{SpendCell: store.SpendCell{Team: "batch", KeyID: "k3", Model: "llama-3.3-70b", Backend: "vllm-internal", Requests: 40}}},
		Budgets: []model.Budget{
			{ID: "b1", ScopeType: "team", Scope: "support", CapUSD: 1000, OnExceed: "block"},
			{ID: "b2", ScopeType: "project", Scope: "p3", CapUSD: 500, OnExceed: "warn"},
		},
		Prices: []store.PriceRow{
			priceRow("gpt-5-mini", "openai-prod", month.AddDate(0, -2, 0), nil, rates(in5, 10)),
			// Not used this month: left out of the basis.
			priceRow("gpt-5.5", "openai-prod", month.AddDate(0, -2, 0), nil, rates(10, 40)),
		},
	}
}

func TestCloseReportTotalsBudgetsAndBasis(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	r := buildCloseReport(closeInputFor(sep, now))
	if r.Title != "Spend close report: September 2026" || r.Open {
		t.Errorf("title %q open %v", r.Title, r.Open)
	}
	if r.TotalUSD != 1334.75 || r.Requests != 1240 || r.Unpriced != 40 {
		t.Errorf("total %v requests %d unpriced %d", r.TotalUSD, r.Requests, r.Unpriced)
	}
	// Raw receipts reach back to Sep 6, so the unpriced count misses Sep 1–5.
	if !r.UnpricedPartial {
		t.Errorf("unpriced count should be marked partial before raw retention")
	}
	if len(r.Sections) != 4 || r.Sections[0].Title != "By team" || r.Sections[0].Rows[0].Label != "Support" {
		t.Fatalf("sections %+v", r.Sections)
	}
	// Budgets: spend in the month against the cap, with what the cap does.
	if len(r.Budgets) != 2 || r.Budgets[0].Name != "Support" || r.Budgets[0].SpentUSD != 1234.5 || !r.Budgets[0].Over {
		t.Errorf("budgets %+v", r.Budgets)
	}
	if b := r.Budgets[1]; b.Name != "digest" || b.SpentUSD != 100.25 || b.Over || b.Unpriced != 40 {
		t.Errorf("project budget %+v", b)
	}
	if len(r.Prices) != 1 || r.Prices[0].ModelID != "gpt-5-mini" {
		t.Errorf("basis rows %+v", r.Prices)
	}

	pdf := r.PDF(now)
	for _, s := range []string{"September 2026", "$1,334.75", "Support", "helpdesk", "support-bot", "llama-3.3-70b", "no price", "Blocks new requests", "Prices"} {
		if !bytes.Contains(pdf, []byte(s)) {
			t.Errorf("PDF is missing %q", s)
		}
	}
	// The llama row has no priced spend: "no price", never $0.00.
	if bytes.Contains(pdf, []byte("($0.00) Tj")) {
		t.Errorf("an unpriced row reads $0.00")
	}
}

// A month older than raw receipts can't say how many requests had no price:
// unknown, not 0. A month with nothing priced reads "no price", not $0.00.
func TestCloseReportPastRawRetentionAndAllUnpriced(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	jul := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	in := closeInputFor(jul, now)
	in.Unpriced = nil
	r := buildCloseReport(in)
	if !r.UnpricedUnknown {
		t.Fatalf("July's unpriced count should be unknown")
	}
	pdf := string(r.PDF(now))
	if !strings.Contains(pdf, "(not known: raw receipts are kept 30 days) Tj") {
		t.Errorf("unknown unpriced count not stated")
	}

	in = closeInputFor(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), now)
	in.Cells = in.Cells[2:] // llama only: 40 requests, none priced
	pdf = string(buildCloseReport(in).PDF(now))
	// The summary's spend, and each section's total.
	if !regexp.MustCompile(`\(Spend\) Tj ET\n[^\n]*\(no price\) Tj`).MatchString(pdf) || regexp.MustCompile(`\(Total\) Tj ET\n[^\n]*\(\$0\.00\) Tj`).MatchString(pdf) {
		t.Errorf("a month with nothing priced reads $0.00")
	}
}

func TestCloseReportForTheOpenMonthSaysSo(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	r := buildCloseReport(closeInputFor(oct, now))
	if !r.Open || r.UnpricedPartial || !r.To.Equal(now) {
		t.Errorf("open %v partial %v to %s", r.Open, r.UnpricedPartial, r.To)
	}
	if !strings.Contains(string(r.PDF(now)), "month to date") {
		t.Errorf("open month not stated")
	}
}
