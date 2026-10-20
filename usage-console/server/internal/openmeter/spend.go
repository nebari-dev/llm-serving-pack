package openmeter

import (
	"cmp"
	"context"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nebari-dev/openmeter-pack/ui/server/internal/api"
)

// ranges are the console's time ranges.
var ranges = map[string]time.Duration{
	"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour,
	"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

func rangeOf(r *http.Request) (string, time.Duration) {
	name := r.URL.Query().Get("range")
	d, ok := ranges[name]
	if !ok {
		return "24h", ranges["24h"]
	}
	return name, d
}

// spendDims maps the console's Spend dimensions to the event field each
// groups by. A key's project is the model resource it was minted for.
var spendDims = map[string]string{
	"team":     "team",
	"person":   "user",
	"key":      "api_key",
	"model":    "model",
	"project":  "model_resource",
	"provider": "provider",
}

func (s *Server) spend(r *http.Request) (any, error) {
	ctx := r.Context()
	name, span := rangeOf(r)
	by := r.URL.Query().Get("by")
	if _, ok := spendDims[by]; !ok {
		by = "team"
	}
	now := s.Now().UTC()
	from, prevFrom := now.Add(-span), now.Add(-2*span)
	out := api.SpendView{Range: name, By: by, From: from.UnixMilli(), To: now.UnixMilli(), PrevFrom: prevFrom.UnixMilli(), Rows: []api.SpendRow{}}

	cur, err := s.usage(ctx, from, now, nil)
	if err != nil {
		return nil, err
	}
	prev, err := s.usage(ctx, prevFrom, from, nil)
	if err != nil {
		return nil, err
	}
	out.Rows = spendRows(by, cur, prev)
	for _, c := range cur {
		if c.Unpriced {
			out.Unpriced += c.Requests
		}
	}
	if out.Trend, err = s.spendTrend(ctx, name, by, now); err != nil {
		return nil, err
	}
	if out.Period, err = s.projection(ctx, now); err != nil {
		return nil, err
	}
	return out, nil
}

// trendBuckets are the Spend chart's bars per range, as Stargate's.
var trendBuckets = map[string]struct {
	bucket time.Duration
	points int
}{
	"15m": {5 * time.Minute, 3},
	"1h":  {5 * time.Minute, 12},
	"6h":  {15 * time.Minute, 24},
	"24h": {time.Hour, 24},
	"7d":  {24 * time.Hour, 7},
	"30d": {24 * time.Hour, 30},
}

// queryWindow is the largest OpenMeter window that fits in a bucket.
func queryWindow(bucket time.Duration) *window {
	switch {
	case bucket >= 24*time.Hour:
		return &byDay
	case bucket >= time.Hour:
		return &byHour
	}
	return &byMinute
}

func (s *Server) spendTrend(ctx context.Context, rangeName, by string, now time.Time) (api.SpendTrend, error) {
	tb := trendBuckets[rangeName]
	start := now.Truncate(tb.bucket).Add(-time.Duration(tb.points-1) * tb.bucket)
	cells, err := s.usage(ctx, start, now, queryWindow(tb.bucket))
	if err != nil {
		return api.SpendTrend{}, err
	}
	field := spendDims[by]
	tr := api.SpendTrend{BucketMS: tb.bucket.Milliseconds(), Points: make([]api.TrendPoint, tb.points), Order: []string{}, Labels: map[string]string{}}
	for i := range tr.Points {
		tr.Points[i] = api.TrendPoint{T: start.Add(time.Duration(i) * tb.bucket).UnixMilli(), Values: map[string]float64{}}
	}
	for _, c := range cells {
		if i := int(c.Start.Sub(start) / tb.bucket); i >= 0 && i < tb.points {
			g := group(c, field)
			tr.Points[i].Values[g] = roundUSD(tr.Points[i].Values[g] + c.USD)
		}
		if c.Unpriced {
			tr.Unpriced += c.Requests
		}
	}
	// Rank groups by 30 days of spend so each keeps its color across ranges.
	month, err := s.usage(ctx, now.Add(-30*24*time.Hour), now, nil)
	if err != nil {
		return tr, err
	}
	totals := map[string]float64{}
	for _, c := range month {
		totals[group(c, field)] += c.USD
	}
	for _, p := range tr.Points {
		for id := range p.Values {
			if _, ok := totals[id]; !ok {
				totals[id] = 0
			}
		}
	}
	for id := range totals {
		tr.Order = append(tr.Order, id)
		tr.Labels[id] = id
	}
	slices.SortFunc(tr.Order, func(a, b string) int { return cmp.Or(cmp.Compare(totals[b], totals[a]), cmp.Compare(a, b)) })
	return tr, nil
}

// projectionDays is the trailing average's span. Stargate shortens it when
// history is newer than that; finding the first usage would mean reading
// every event, so here it is always the last 7 days.
const projectionDays = 7

// projection is month-end spend: this UTC month to date, plus the trailing
// daily average for each day left.
func (s *Server) projection(ctx context.Context, now time.Time) (api.Projection, error) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	p := api.Projection{PeriodStart: start.UnixMilli(), PeriodEnd: end.UnixMilli(), TrailingDays: projectionDays, RemainingDays: end.Sub(now).Hours() / 24}
	mtd, err := s.usage(ctx, start, now, nil)
	if err != nil {
		return p, err
	}
	for _, c := range mtd {
		p.MonthToDateUSD += c.USD
		if c.Unpriced {
			p.Unpriced += c.Requests
		}
	}
	trailing, err := s.usage(ctx, now.Add(-projectionDays*24*time.Hour), now, nil)
	if err != nil {
		return p, err
	}
	for _, c := range trailing {
		p.TrailingDailyUSD += c.USD
	}
	p.TrailingDailyUSD /= projectionDays
	p.MonthToDateUSD = roundUSD(p.MonthToDateUSD)
	p.ProjectedUSD = p.MonthToDateUSD + p.TrailingDailyUSD*p.RemainingDays
	return p, nil
}

// spendRows sums cells by the dimension, biggest spend first.
func spendRows(by string, cur, prev []cell) []api.SpendRow {
	field := spendDims[by]
	rows := map[string]*api.SpendRow{}
	row := func(id string) *api.SpendRow {
		if rows[id] == nil {
			rows[id] = &api.SpendRow{ID: id, Label: id}
		}
		return rows[id]
	}
	for _, c := range cur {
		x := row(group(c, field))
		x.SpendUSD += c.USD
		x.Requests += c.Requests
		x.Tokens += c.Tokens
		if c.Unpriced {
			x.Unpriced += c.Requests
		}
	}
	for _, c := range prev {
		row(group(c, field)).PrevSpendUSD += c.USD
	}
	out := []api.SpendRow{}
	for _, x := range rows {
		if user, ok := strings.CutPrefix(x.ID, loginKey+":"); ok {
			// Traffic filters by key, and every login shares one.
			x.Label, x.NoDrill = user+" (Keycloak login)", true
		}
		x.SpendUSD, x.PrevSpendUSD = roundUSD(x.SpendUSD), roundUSD(x.PrevSpendUSD)
		out = append(out, *x)
	}
	slices.SortFunc(out, func(a, b api.SpendRow) int {
		return cmp.Or(cmp.Compare(b.SpendUSD, a.SpendUSD), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// group is the cell's group under field. Under api_key, requests made with
// a Keycloak login group by person: they share the collector's "login" key.
func group(c cell, field string) string {
	if field == "api_key" && c.Key == loginKey {
		return loginKey + ":" + c.User
	}
	return c.dim(field)
}

// roundUSD drops float noise below a millionth of a dollar.
func roundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }
