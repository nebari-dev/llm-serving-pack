package api

import (
	"cmp"
	"context"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// §7.5.5 Spend. Every number here comes from receipts_5m and receipts_daily
// (§4.6); nothing reads raw receipts.

// spendTrendBuckets is the trend chart's grain per range: from receipts_5m
// under a day, daily above.
var spendTrendBuckets = map[string]struct {
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

// projectionDays is how many trailing days the projection averages.
const projectionDays = 7

var spendDims = []string{"team", "project", "key", "model", "provider"}

type SpendView struct {
	Range string `json:"range"`
	By    string `json:"by"`
	// Totals and rows cover [from, to); the deltas compare with the same span
	// before it, [prevFrom, from).
	From     int64      `json:"from"`
	To       int64      `json:"to"`
	PrevFrom int64      `json:"prevFrom"`
	Rows     []SpendRow `json:"rows"`
	// Unpriced counts served requests in [from, to) whose (model, backend)
	// had no price: the totals leave them out until one is set.
	Unpriced int        `json:"unpriced"`
	Trend    SpendTrend `json:"trend"`
	Period   Projection `json:"period"`
}

// SpendRow is one group in the breakdown. ID is what Traffic filters on.
type SpendRow struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Sub          string  `json:"sub,omitempty"`
	SpendUSD     float64 `json:"spendUsd"`
	PrevSpendUSD float64 `json:"prevSpendUsd"`
	Requests     int     `json:"requests"`
	Tokens       int     `json:"tokens"`
	// Unpriced counts the group's served requests with no price: in
	// Requests, not in SpendUSD.
	Unpriced int `json:"unpriced"`
	// NoDrill is set when Traffic has no filter for the group: requests
	// without a key identity, or refused before routing.
	NoDrill bool `json:"noDrill,omitempty"`
}

type SpendTrend struct {
	BucketMS int64        `json:"bucketMs"`
	Points   []TrendPoint `json:"points"`
	// Order ranks every group by its last 30 days of spend, so a group keeps
	// its color when the range changes.
	Order  []string          `json:"order"`
	Labels map[string]string `json:"labels"`
	// Unpriced counts requests over the charted buckets that have no price,
	// so aren't in the bars.
	Unpriced int `json:"unpriced"`
}

type TrendPoint struct {
	T      int64              `json:"t"`
	Values map[string]float64 `json:"values"`
}

// Projection is month-end spend and its basis: month to date plus the
// trailing daily average for each day left. The month is the UTC calendar
// month. TrailingDays is under projectionDays when there's less history.
// Unpriced counts the month's requests with no price, which it leaves out.
type Projection struct {
	PeriodStart      int64   `json:"periodStart"`
	PeriodEnd        int64   `json:"periodEnd"`
	MonthToDateUSD   float64 `json:"monthToDateUsd"`
	TrailingDailyUSD float64 `json:"trailingDailyUsd"`
	TrailingDays     float64 `json:"trailingDays"`
	RemainingDays    float64 `json:"remainingDays"`
	ProjectedUSD     float64 `json:"projectedUsd"`
	Unpriced         int     `json:"unpriced"`
}

// grouper names the group a cell belongs to under each dimension, using the
// catalog for what the aggregates don't carry: a key's name and project, a
// backend's provider.
type grouper struct {
	teams    map[string]model.Team
	keys     map[string]model.APIKey
	backends map[string]model.Backend
	models   map[string]model.Model
	projects map[string]model.Project // deleted ones too, for their history
}

func (s *Server) grouper(ctx context.Context, t string) (grouper, error) {
	g := grouper{teams: map[string]model.Team{}, keys: map[string]model.APIKey{}, backends: map[string]model.Backend{}, models: map[string]model.Model{},
		projects: map[string]model.Project{}}
	ps, err := s.Store.Projects(ctx, t)
	if err != nil {
		return g, err
	}
	for _, x := range ps {
		g.projects[x.ID] = x
	}
	teams, err := s.Store.Teams(ctx, t)
	if err != nil {
		return g, err
	}
	for _, x := range teams {
		g.teams[x.ID] = x
	}
	keys, err := s.Store.Keys(ctx, t)
	if err != nil {
		return g, err
	}
	for _, x := range keys {
		g.keys[x.ID] = x.APIKey
	}
	bs, err := s.Store.Backends(ctx, t)
	if err != nil {
		return g, err
	}
	for _, x := range bs {
		g.backends[x.Name] = x
	}
	ms, err := s.Store.Models(ctx)
	if err != nil {
		return g, err
	}
	for _, x := range ms {
		g.models[x.ID] = x
	}
	return g, nil
}

// budgetScopes are the budget scopes a cell's spend counts toward: its team,
// and its key and the key's project, by id as budgets name them.
func (g grouper) budgetScopes(c store.SpendCell) []string {
	ids := []string{"team:" + c.Team}
	if k, ok := g.keys[c.KeyID]; ok {
		ids = append(ids, "key:"+k.ID, "project:"+k.ProjectID)
	}
	return ids
}

// unpricedByScope counts unpriced requests per budget scope id.
func unpricedByScope(g grouper, cells []store.UnpricedCell) map[string]int {
	out := map[string]int{}
	for _, c := range cells {
		for _, id := range g.budgetScopes(c.SpendCell) {
			out[id] += c.Requests
		}
	}
	return out
}

// scopeName is how the console shows a budget's scope: a key's or project's
// name (projects maps id to name, for projects with no keys yet), the id
// when it's gone, and a team's id as it is.
func (g grouper) scopeName(b model.Budget, projects map[string]string) string {
	switch b.ScopeType {
	case "key":
		if k, ok := g.keys[b.Scope]; ok {
			return k.Name
		}
	case "project":
		if n, ok := projects[b.Scope]; ok {
			return n
		}
	}
	return b.Scope
}

// notRouted groups requests refused before routing. The dev gateway records
// their backend as "" and receipt-ingest as "—".
const notRouted = "(not routed)"

// unattributed groups receipts with no key identity.
const unattributed = "(unattributed)"

// key returns the group id for a cell. Keys group by name, since that's what
// Traffic's key filter takes. Projects group by id (§5.1), so two teams'
// same-named projects are two rows and a rename keeps one; the aggregates
// keep the key, and a key never changes project.
func (g grouper) key(c store.SpendCell, by string) string {
	if c.KeyID == "" && by != "model" && by != "provider" {
		return unattributed
	}
	switch by {
	case "project":
		if k, ok := g.keys[c.KeyID]; ok {
			return k.ProjectID
		}
		return "(unknown key)"
	case "key":
		if k, ok := g.keys[c.KeyID]; ok {
			return k.Name
		}
		return c.KeyID
	case "model":
		return c.Model
	case "provider":
		if b, ok := g.backends[c.Backend]; ok {
			return b.Provider
		}
		if c.Backend == "" || c.Backend == "—" {
			return notRouted
		}
		return c.Backend
	}
	return c.Team
}

func (g grouper) label(id, by string) (label, sub string) {
	if id == unattributed {
		return "Unattributed", "no key identity on the receipt"
	}
	switch by {
	case "team":
		if t, ok := g.teams[id]; ok {
			return t.Name, t.CostCenter
		}
	case "project":
		if p, ok := g.projects[id]; ok {
			sub = p.Team
			if t, ok := g.teams[p.Team]; ok {
				sub = t.Name
			}
			if p.Deleted {
				sub += " · deleted"
			}
			return p.Name, sub
		}
	case "key":
		for _, k := range g.keys {
			if k.Name == id {
				project := k.Project
				if p, ok := g.projects[k.ProjectID]; ok {
					project = p.Name
				}
				return id, k.Team + " / " + project
			}
		}
	case "model":
		if m, ok := g.models[id]; ok {
			return id, m.Provider
		}
	case "provider":
		if id == notRouted {
			return "Not routed", "refused before a backend was chosen"
		}
		for _, b := range g.backends {
			if b.Provider == id {
				return id, ""
			}
		}
		return id, "backend no longer configured"
	}
	return id, ""
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (s *Server) spend(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	name, span := rangeDuration(r)
	by := r.URL.Query().Get("by")
	if !slices.Contains(spendDims, by) {
		by = "team"
	}
	g, err := s.grouper(ctx, t)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	from, prevFrom := now.Add(-span), now.Add(-2*span)
	out := SpendView{Range: name, By: by, From: from.UnixMilli(), To: now.UnixMilli(), PrevFrom: prevFrom.UnixMilli(), Rows: []SpendRow{}}

	cur, err := s.Store.SpendCells(ctx, t, from, now)
	if err != nil {
		return nil, err
	}
	unpriced, err := s.Store.UnpricedCells(ctx, t, from, now, 0)
	if err != nil {
		return nil, err
	}
	prev, err := s.Store.SpendCells(ctx, t, prevFrom, from)
	if err != nil {
		return nil, err
	}
	out.Rows = spendRows(g, by, cur, prev, unpriced)
	for _, c := range unpriced {
		out.Unpriced += c.Requests
	}

	if out.Trend, err = s.spendTrend(ctx, t, name, by, g, now); err != nil {
		return nil, err
	}
	if out.Period, _, err = s.projection(ctx, t, now, nil); err != nil {
		return nil, err
	}
	return out, nil
}

// spendRows groups the window's cells (and the previous window's, for the
// deltas) into the breakdown, biggest spend first.
func spendRows(g grouper, by string, cur, prev []store.SpendCell, unpriced []store.UnpricedCell) []SpendRow {
	rows := map[string]*SpendRow{}
	row := func(id string) *SpendRow {
		if rows[id] == nil {
			rows[id] = &SpendRow{ID: id}
		}
		return rows[id]
	}
	for _, c := range cur {
		x := row(g.key(c, by))
		x.SpendUSD += c.USD
		x.Requests += c.Requests
		x.Tokens += c.Tokens
	}
	for _, c := range prev {
		row(g.key(c, by)).PrevSpendUSD += c.USD
	}
	for _, c := range unpriced {
		row(g.key(c.SpendCell, by)).Unpriced += c.Requests
	}
	out := []SpendRow{}
	for id, x := range rows {
		x.Label, x.Sub = g.label(id, by)
		x.NoDrill = id == unattributed || id == notRouted || id == "(unknown key)"
		x.SpendUSD, x.PrevSpendUSD = round2(x.SpendUSD), round2(x.PrevSpendUSD)
		out = append(out, *x)
	}
	slices.SortFunc(out, func(a, b SpendRow) int { return cmp.Or(cmp.Compare(b.SpendUSD, a.SpendUSD), cmp.Compare(a.ID, b.ID)) })
	return out
}

func (s *Server) spendTrend(ctx context.Context, t, rangeName, by string, g grouper, now time.Time) (SpendTrend, error) {
	tb := spendTrendBuckets[rangeName]
	start := now.Truncate(tb.bucket).Add(-time.Duration(tb.points-1) * tb.bucket)
	buckets, err := s.Store.SpendBuckets(ctx, t, start, now, tb.bucket)
	if err != nil {
		return SpendTrend{}, err
	}
	tr := SpendTrend{BucketMS: tb.bucket.Milliseconds(), Points: make([]TrendPoint, tb.points), Order: []string{}, Labels: map[string]string{}}
	for i := range tr.Points {
		tr.Points[i] = TrendPoint{T: start.Add(time.Duration(i) * tb.bucket).UnixMilli(), Values: map[string]float64{}}
	}
	for _, b := range buckets {
		if i := int(b.Start.Sub(start) / tb.bucket); i >= 0 && i < tb.points {
			tr.Points[i].Values[g.key(b.SpendCell, by)] += b.USD
		}
	}
	if tr.Unpriced, err = s.Store.UnpricedCount(ctx, t, start, now); err != nil {
		return tr, err
	}
	month, err := s.Store.SpendCells(ctx, t, now.Add(-30*24*time.Hour), now)
	if err != nil {
		return tr, err
	}
	totals := map[string]float64{}
	for _, c := range month {
		totals[g.key(c, by)] += c.USD
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
		tr.Labels[id], _ = g.label(id, by)
	}
	slices.SortFunc(tr.Order, func(a, b string) int { return cmp.Or(cmp.Compare(totals[b], totals[a]), cmp.Compare(a, b)) })
	return tr, nil
}

// monthBounds is the UTC calendar month containing now.
func monthBounds(now time.Time) (start, end time.Time) {
	now = now.UTC()
	start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

// project applies the basis to month-to-date spend.
func project(mtd, trailingDaily, remainingDays float64) float64 {
	return mtd + trailingDaily*remainingDays
}

// trailingWindow is the span the projection averages: the last
// projectionDays, or less when receipts start more recently than that.
func trailingWindow(now, firstDay time.Time) (from time.Time, days float64) {
	from = now.Add(-projectionDays * 24 * time.Hour)
	if !firstDay.IsZero() && firstDay.After(from) {
		from = firstDay
	}
	return from, now.Sub(from).Hours() / 24
}

// projection returns the tenant-wide projection and, when scope is non-nil,
// the same basis per scope id: month to date and trailing daily spend for
// each id the scope function returns for a cell.
func (s *Server) projection(ctx context.Context, t string, now time.Time, scope func(store.SpendCell) []string) (Projection, map[string][2]float64, error) {
	start, end := monthBounds(now)
	first, err := s.Store.FirstSpendDay(ctx, t)
	if err != nil {
		return Projection{}, nil, err
	}
	trailFrom, trailDays := trailingWindow(now, first)
	mtd, err := s.Store.SpendCells(ctx, t, start, now)
	if err != nil {
		return Projection{}, nil, err
	}
	trail, err := s.Store.SpendCells(ctx, t, trailFrom, now)
	if err != nil {
		return Projection{}, nil, err
	}
	p := Projection{PeriodStart: start.UnixMilli(), PeriodEnd: end.UnixMilli(), TrailingDays: math.Round(trailDays*10) / 10, RemainingDays: end.Sub(now).Hours() / 24}
	if p.Unpriced, err = s.Store.UnpricedCount(ctx, t, start, now); err != nil {
		return p, nil, err
	}
	per := map[string][2]float64{}
	for _, c := range mtd {
		p.MonthToDateUSD += c.USD
		if scope != nil {
			for _, id := range scope(c) {
				v := per[id]
				v[0] += c.USD
				per[id] = v
			}
		}
	}
	var trailing float64
	for _, c := range trail {
		trailing += c.USD
		if scope != nil {
			for _, id := range scope(c) {
				v := per[id]
				v[1] += c.USD
				per[id] = v
			}
		}
	}
	if trailDays > 0 {
		p.TrailingDailyUSD = trailing / trailDays
		for id, v := range per {
			per[id] = [2]float64{v[0], v[1] / trailDays}
		}
	}
	p.ProjectedUSD = round2(project(p.MonthToDateUSD, p.TrailingDailyUSD, p.RemainingDays))
	p.MonthToDateUSD, p.TrailingDailyUSD = round2(p.MonthToDateUSD), round2(p.TrailingDailyUSD)
	return p, per, nil
}

// budgets adds month-to-date spend and a month-end projection on the same
// basis as the Spend page's.
func (s *Server) budgets(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	bs, err := s.Store.Budgets(r.Context(), t)
	if err != nil {
		return nil, err
	}
	return s.budgetViews(r.Context(), t, bs)
}

// budgetViews fills in spend, projection and version for budgets that may
// not be stored yet (a dry run's).
func (s *Server) budgetViews(ctx context.Context, t string, bs []model.Budget) ([]model.Budget, error) {
	g, err := s.grouper(ctx, t)
	if err != nil {
		return nil, err
	}
	projects := make(map[string]string, len(g.projects))
	for _, p := range g.projects {
		projects[p.ID] = p.Name
	}
	now := time.Now().UTC()
	p, per, err := s.projection(ctx, t, now, g.budgetScopes)
	if err != nil {
		return nil, err
	}
	month, _ := monthBounds(now)
	cells, err := s.Store.UnpricedCells(ctx, t, month, now, 0)
	if err != nil {
		return nil, err
	}
	unpriced := unpricedByScope(g, cells)
	for i := range bs {
		b := &bs[i]
		b.ScopeName = g.scopeName(*b, projects)
		if b.OnExceed == "throttle" {
			b.ThrottlePerMinute = gateway.ThrottleRate
		}
		v := per[b.ScopeType+":"+b.Scope]
		b.CurrentUSD, b.TrailingDailyUSD = round2(v[0]), round2(v[1])
		b.UnpricedRequests = unpriced[b.ScopeType+":"+b.Scope]
		b.ProjectedUSD = round2(project(v[0], v[1], p.RemainingDays))
		b.ETag = store.BudgetETag(*b)
	}
	return bs, nil
}
