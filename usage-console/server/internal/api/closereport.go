package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pdf"
	"github.com/jbouder/stargate/server/internal/pricing"
	"github.com/jbouder/stargate/server/internal/store"
)

// §7.5.5 export: a month's close report as a PDF a finance team can attach
// to the close. Spend by team, project, key and model from the aggregates
// (full history), budgets against their caps, unpriced requests noted, and
// the prices behind it. Each export writes an audit row.

// rawRetention is how long raw receipts are kept (§4.6): what unpriced
// counts and the savings analysis can reach.
const rawRetention = 30 * 24 * time.Hour

// parseCloseMonth reads YYYY-MM as the first instant of that UTC month. The
// current month is allowed (to date); a later one isn't.
func parseCloseMonth(s string, now time.Time) (time.Time, error) {
	m, err := time.Parse("2006-01", s)
	if err != nil || len(s) != 7 {
		return time.Time{}, errors.New("month must be YYYY-MM")
	}
	if cur, _ := monthBounds(now); m.After(cur) {
		return time.Time{}, fmt.Errorf("%s hasn't started yet", s)
	}
	return m, nil
}

type closeInput struct {
	Tenant, Actor string
	// Month is the month's first instant; RawFrom is how far back raw
	// receipts reach.
	Month, Now, RawFrom time.Time
	G                   grouper
	Cells               []store.SpendCell
	// Unpriced covers the month from RawFrom on.
	Unpriced []store.UnpricedCell
	Budgets  []model.Budget
	Prices   []store.PriceRow
}

type closeReport struct {
	Title, Tenant, Actor string
	From, To             time.Time
	// Open: the month isn't over, so the report is month to date.
	Open                bool
	TotalUSD            float64
	Requests, Tokens    int
	Unpriced            int
	UnpricedPartial     bool // raw receipts don't reach back to From
	UnpricedFrom        time.Time
	UnpricedUnknown     bool // raw receipts don't reach the month at all
	Sections            []closeSection
	Budgets             []closeBudget
	Prices              []store.PriceRow // in effect during the month, for pairs with requests
	monthName, monthKey string
}

type closeSection struct {
	Title, Dim string
	Rows       []SpendRow
}

type closeBudget struct {
	Name, ScopeType string
	CapUSD          float64
	SpentUSD        float64
	Over            bool
	Unpriced        int
	Action          string
}

func buildCloseReport(in closeInput) closeReport {
	_, end := monthBounds(in.Month)
	r := closeReport{Tenant: in.Tenant, Actor: in.Actor, From: in.Month, To: end,
		monthName: in.Month.Format("January 2006"), monthKey: in.Month.Format("2006-01")}
	r.Title = "Spend close report: " + r.monthName
	if in.Now.Before(end) {
		r.Open, r.To = true, in.Now
	}
	switch {
	case !in.RawFrom.Before(r.To):
		r.UnpricedUnknown = true
	case in.RawFrom.After(r.From):
		r.UnpricedPartial, r.UnpricedFrom = true, in.RawFrom
	}
	used := map[[2]string]bool{}
	for _, c := range in.Cells {
		r.TotalUSD += c.USD
		r.Requests += c.Requests
		r.Tokens += c.Tokens
		if c.Requests > 0 {
			used[[2]string{c.Model, c.Backend}] = true
		}
	}
	r.TotalUSD = round2(r.TotalUSD)
	for _, c := range in.Unpriced {
		r.Unpriced += c.Requests
	}
	for _, d := range []struct{ title, dim string }{{"By team", "team"}, {"By project", "project"}, {"By key", "key"}, {"By model", "model"}} {
		r.Sections = append(r.Sections, closeSection{Title: d.title, Dim: d.dim, Rows: spendRows(in.G, d.dim, in.Cells, nil, in.Unpriced)})
	}

	spent := map[string]float64{}
	for _, c := range in.Cells {
		for _, id := range in.G.budgetScopes(c) {
			spent[id] += c.USD
		}
	}
	unpriced := unpricedByScope(in.G, in.Unpriced)
	for _, b := range in.Budgets {
		id := b.ScopeType + ":" + b.Scope
		cb := closeBudget{Name: b.Scope, ScopeType: b.ScopeType, CapUSD: b.CapUSD, SpentUSD: round2(spent[id]), Unpriced: unpriced[id]}
		switch b.ScopeType {
		case "team":
			if t, ok := in.G.teams[b.Scope]; ok {
				cb.Name = t.Name
			}
		case "project":
			if p, ok := in.G.projects[b.Scope]; ok {
				cb.Name = p.Name
			}
		case "key":
			if k, ok := in.G.keys[b.Scope]; ok {
				cb.Name = k.Name
			}
		}
		cb.Over = cb.SpentUSD >= b.CapUSD
		switch b.OnExceed {
		case "block":
			cb.Action = "Blocks new requests at " + cents(b.CapUSD)
		case "throttle":
			cb.Action = fmt.Sprintf("Throttles each key to %d requests a minute at %s", gateway.ThrottleRate, cents(b.CapUSD))
		default:
			cb.Action = "Marks requests over " + cents(b.CapUSD) + " in their receipts"
		}
		r.Budgets = append(r.Budgets, cb)
	}

	for _, p := range in.Prices {
		if used[[2]string{p.ModelID, p.Backend}] && p.From.Before(r.To) && (p.To == nil || p.To.After(r.From)) {
			r.Prices = append(r.Prices, p)
		}
	}
	slices.SortFunc(r.Prices, func(a, b store.PriceRow) int {
		return strings.Compare(a.ModelID+"\x00"+a.Backend+"\x00"+a.From.Format(time.RFC3339), b.ModelID+"\x00"+b.Backend+"\x00"+b.From.Format(time.RFC3339))
	})
	return r
}

// cents is dollars and cents with thousands separators: "$1,334.75".
func cents(v float64) string {
	c := int64(math.Round(math.Abs(v) * 100))
	s := fmt.Sprintf("%d", c/100)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	sign := ""
	if v < 0 && c > 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s$%s.%02d", sign, s, c%100)
}

func count(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

const utcStamp = "Jan 2, 2006 15:04 UTC"

// PDF lays the report out for print.
func (r closeReport) PDF(now time.Time) []byte {
	l := pdf.NewLayout(r.Title)
	l.Title(r.Title)
	lastDay := r.To.Add(-time.Nanosecond)
	period := fmt.Sprintf("%s to %s (UTC)", r.From.Format("Jan 2, 2006"), lastDay.Format("Jan 2, 2006"))
	if r.Open {
		period = fmt.Sprintf("%s to %s, month to date", r.From.Format("Jan 2, 2006"), r.To.Format(utcStamp))
	}
	l.Para(fmt.Sprintf("Tenant %s · %s · generated %s by %s", r.Tenant, period, now.UTC().Format(utcStamp), r.Actor), 8.5)
	if r.Open {
		l.Bold("This month is still open: every figure is month to date, and will change until the month ends.", 8.5)
	}

	// No priced spend but unpriced requests: "no price", never $0.
	moneyOr := func(row SpendRow) string {
		if row.Unpriced > 0 && row.SpendUSD == 0 {
			return "no price"
		}
		return cents(row.SpendUSD)
	}
	// Unpriced counts come from raw receipts; a month they don't reach has none.
	unpricedText := func(n int) string {
		if r.UnpricedUnknown {
			return "not known"
		}
		return count(n)
	}
	total := moneyOr(SpendRow{SpendUSD: r.TotalUSD, Unpriced: r.Unpriced})

	l.Heading("Summary")
	l.Pair("Spend", total, 9.5)
	l.Pair("Requests", count(r.Requests), 9.5)
	l.Pair("Tokens", count(r.Tokens), 9.5)
	if r.UnpricedUnknown {
		l.Pair("Requests with no price", "not known: raw receipts are kept 30 days", 9.5)
	} else {
		l.Pair("Requests with no price", count(r.Unpriced)+" (in requests, not in any spend)", 9.5)
	}
	l.Space(4)
	l.Para("Spend is what was priced. A request whose model and backend had no price has no cost: it is counted in requests "+
		"and shown as \"no price\", never as $0, until a price is set for that pair.", 8.5)
	switch {
	case r.UnpricedUnknown:
		l.Para("Requests with no price are counted from raw receipts, which are kept 30 days, so this month's can no longer be counted. "+
			"Spend and requests come from the aggregates and are complete.", 8.5)
	case r.UnpricedPartial:
		l.Para(fmt.Sprintf("Requests with no price are counted from raw receipts, which are kept 30 days: this count covers %s on and misses any before.",
			r.UnpricedFrom.Format(utcStamp)), 8.5)
	}

	l.Heading("Price basis")
	l.Para("Each request is costed when it settles, at the price for its model and backend in effect when the request started, "+
		"and that price is stored on its receipt, so a later price change never reprices it. A request that arrived with no price "+
		"and was priced once a rate was set uses that rate. Prices are dollars per million tokens, from LiteLLM's price list "+
		"or a manual override per model and backend; cached input and cache writes are part of input, reasoning part of output, "+
		"each at its own rate. The rows in effect this month are listed at the end. Spend comes from the daily receipt aggregates, which keep full history.", 8.5)

	for _, s := range r.Sections {
		l.Heading(s.Title)
		if len(s.Rows) == 0 {
			l.Para("No requests.", 8.5)
			continue
		}
		cols := []pdf.Col{{Title: strings.TrimPrefix(s.Title, "By "), Width: 140}, {Title: "Detail", Width: 134}, {Title: "Spend", Width: 70, Right: true},
			{Title: "Requests", Width: 54, Right: true}, {Title: "No price", Width: 46, Right: true}, {Title: "Tokens", Width: 60, Right: true}}
		cols[0].Title = strings.ToUpper(cols[0].Title[:1]) + cols[0].Title[1:]
		var rows [][]string
		unpriced := 0
		for _, row := range s.Rows {
			unpriced += row.Unpriced
			rows = append(rows, []string{row.Label, row.Sub, moneyOr(row), count(row.Requests), unpricedText(row.Unpriced), count(row.Tokens)})
		}
		l.Table(cols, rows, []string{"Total", "", total, count(r.Requests), unpricedText(unpriced), count(r.Tokens)})
	}

	l.Heading("Budgets")
	if len(r.Budgets) == 0 {
		l.Para("No budgets set.", 8.5)
	} else {
		l.Para("Monthly caps, UTC, as configured when this report was made. Spent is this month's priced spend in the budget's scope; "+
			"requests with no price don't count toward a cap.", 8.5)
		cols := []pdf.Col{{Title: "Scope", Width: 110}, {Title: "Type", Width: 44}, {Title: "Cap", Width: 64, Right: true}, {Title: "Spent", Width: 64, Right: true},
			{Title: "Of cap", Width: 40, Right: true}, {Title: "No price", Width: 40, Right: true}, {Title: "At the cap", Width: 142}}
		var rows [][]string
		for _, b := range r.Budgets {
			share := "—"
			if b.CapUSD > 0 {
				share = fmt.Sprintf("%.0f%%", b.SpentUSD/b.CapUSD*100)
			}
			action := b.Action
			if b.Over {
				action = "Over cap. " + action
			}
			rows = append(rows, []string{b.Name, b.ScopeType, cents(b.CapUSD), cents(b.SpentUSD), share, unpricedText(b.Unpriced), action})
		}
		l.Table(cols, rows, nil)
	}

	l.Heading("Prices in effect")
	if len(r.Prices) == 0 {
		l.Para("No priced model and backend had requests this month.", 8.5)
	} else {
		rate := func(v *float64) string {
			if v == nil {
				return "no price"
			}
			return fmt.Sprintf("$%.4g", *v)
		}
		cols := []pdf.Col{{Title: "Model", Width: 92}, {Title: "Backend", Width: 78}, {Title: "In effect", Width: 106}, {Title: "Input", Width: 44, Right: true},
			{Title: "Cached", Width: 42, Right: true}, {Title: "Cache write", Width: 48, Right: true}, {Title: "Output", Width: 46, Right: true}, {Title: "Reasoning", Width: 48, Right: true}}
		var rows [][]string
		for _, p := range r.Prices {
			span := maxTime(p.From, r.From).Format("Jan 2 15:04") + " on"
			if p.To != nil && p.To.Before(r.To) {
				span = maxTime(p.From, r.From).Format("Jan 2 15:04") + " – " + p.To.Format("Jan 2 15:04")
			}
			rows = append(rows, []string{p.ModelID, p.Backend, span, rate(p.Rates[pricing.Input]), rate(p.Rates[pricing.CachedInput]),
				rate(p.Rates[pricing.CacheWrite]), rate(p.Rates[pricing.Output]), rate(p.Rates[pricing.Reasoning])})
		}
		l.Table(cols, rows, nil)
		l.Para("Dollars per million tokens. Sources by rate (LiteLLM or manual) are on each receipt's price snapshot and on Models > Pricing.", 7.5)
	}
	return l.Bytes(now, fmt.Sprintf("Stargate · %s · close report %s · generated %s", r.Tenant, r.monthKey, now.UTC().Format(utcStamp)))
}

func (s *Server) closeReport(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	now := time.Now().UTC()
	month, err := parseCloseMonth(r.URL.Query().Get("month"), now)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	_, end := monthBounds(month)
	to := end
	if now.Before(end) {
		to = now
	}
	in := closeInput{Tenant: t, Actor: s.DevActor, Month: month, Now: now, RawFrom: now.Add(-rawRetention)}
	if in.G, err = s.grouper(ctx, t); err != nil {
		return nil, err
	}
	if in.Cells, err = s.Store.SpendCells(ctx, t, month, to); err != nil {
		return nil, err
	}
	if from := maxTime(month, in.RawFrom); from.Before(to) {
		if in.Unpriced, err = s.Store.UnpricedCells(ctx, t, from, to, 0); err != nil {
			return nil, err
		}
	}
	if in.Budgets, err = s.Store.Budgets(ctx, t); err != nil {
		return nil, err
	}
	if in.Prices, err = s.Store.PriceRows(ctx); err != nil {
		return nil, err
	}
	rep := buildCloseReport(in)
	var file []byte
	after := map[string]any{"month": rep.monthKey, "spendUsd": rep.TotalUSD, "requests": rep.Requests, "unpricedRequests": rep.Unpriced, "monthToDate": rep.Open}
	// The audit row commits only once the file is made.
	if err := s.Store.Audited(ctx, t, s.DevActor, "Exported close report", rep.monthKey, "Export", nil, after, func() error {
		file = rep.PDF(now)
		return nil
	}); err != nil {
		return nil, err
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="stargate-close-report-%s-%s.pdf"`, t, rep.monthKey))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(file)
	return nil, nil
}
