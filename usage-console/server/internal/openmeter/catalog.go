package openmeter

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/model"
)

// The catalog is what usage shows: teams, projects (a team's model
// resources), models and API keys seen in the last 30 days. Nothing here is
// stored; there is nothing to create or edit.
const catalogSpan = 30 * 24 * time.Hour

// loginKey is the collector's api_key for requests made with a Keycloak
// token instead of an API key.
const loginKey = "login"

func projectID(team, resource string) string { return team + "/" + resource }

func (s *Server) recent(ctx context.Context) ([]cell, error) {
	now := s.Now().UTC()
	return s.usage(ctx, now.Add(-catalogSpan), now, nil)
}

func (s *Server) teams(r *http.Request) (any, error) {
	cells, err := s.recent(r.Context())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []model.Team{}
	for _, c := range cells {
		if c.Team != "" && !seen[c.Team] {
			seen[c.Team] = true
			out = append(out, model.Team{ID: c.Team, Name: c.Team})
		}
	}
	slices.SortFunc(out, func(a, b model.Team) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (s *Server) projects(r *http.Request) (any, error) {
	cells, err := s.recent(r.Context())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []model.Project{}
	for _, c := range cells {
		id := projectID(c.Team, c.Resource)
		if c.Resource != "" && !seen[id] {
			seen[id] = true
			out = append(out, model.Project{ID: id, Team: c.Team, Name: c.Resource})
		}
	}
	slices.SortFunc(out, func(a, b model.Project) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (s *Server) models(r *http.Request) (any, error) {
	ctx := r.Context()
	cells, err := s.recent(ctx)
	if err != nil {
		return nil, err
	}
	prices, err := s.pricer(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []model.Model{}
	for _, c := range cells {
		if c.Model != "" && !seen[c.Model] {
			seen[c.Model] = true
			out = append(out, model.Model{ID: c.Model, Provider: prices.providers[c.Resource]})
		}
	}
	slices.SortFunc(out, func(a, b model.Model) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// keys are the API keys used in the last 30 days, with their last 24 hours.
// LastUsedAt is the start of the hour (or, idle for a day, the day) of the
// key's last request: OpenMeter's windows are as fine as it gets without
// reading every event.
func (s *Server) keys(r *http.Request) (any, error) {
	ctx := r.Context()
	now := s.Now().UTC()
	month, err := s.usage(ctx, now.Add(-catalogSpan), now, &byDay)
	if err != nil {
		return nil, err
	}
	dayStart := now.Truncate(time.Hour).Add(-23 * time.Hour)
	day, err := s.usage(ctx, dayStart, now, &byHour)
	if err != nil {
		return nil, err
	}
	by := map[string]*model.APIKey{}
	key := func(c cell) *model.APIKey {
		k := by[c.Key]
		if k == nil {
			k = &model.APIKey{
				ID: c.Key, Name: c.Key, Prefix: c.Key, Team: c.Team, Owner: c.User,
				Project: c.Resource, ProjectID: projectID(c.Team, c.Resource),
				Hourly24h: make([]int, 24),
			}
			by[c.Key] = k
		}
		return k
	}
	used := func(k *model.APIKey, at time.Time) {
		ms := at.UnixMilli()
		if k.LastUsedAt == nil || ms > *k.LastUsedAt {
			k.LastUsedAt = &ms
		}
	}
	for _, c := range month {
		if c.Key == "" || c.Key == loginKey {
			continue
		}
		if k := key(c); c.Requests > 0 {
			used(k, c.Start)
		}
	}
	for _, c := range day {
		if c.Key == "" || c.Key == loginKey {
			continue
		}
		k := key(c)
		k.Requests24h += c.Requests
		k.Spend24hUSD = roundUSD(k.Spend24hUSD + c.USD)
		if c.Unpriced {
			k.Unpriced24h += c.Requests
		}
		if i := int(c.Start.Sub(dayStart) / time.Hour); i >= 0 && i < 24 {
			k.Hourly24h[i] += c.Requests
		}
		if c.Requests > 0 {
			used(k, c.Start)
		}
	}
	out := []model.APIKey{}
	for _, k := range by {
		out = append(out, *k)
	}
	slices.SortFunc(out, func(a, b model.APIKey) int {
		return cmp.Or(cmp.Compare(b.Requests24h, a.Requests24h), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// trafficBuckets are the Overview chart's points per range.
var trafficBuckets = map[string]struct {
	bucket time.Duration
	points int
}{
	"15m": {5 * time.Minute, 3},
	"1h":  {5 * time.Minute, 12},
	"6h":  {15 * time.Minute, 24},
	"24h": {30 * time.Minute, 48},
	"7d":  {6 * time.Hour, 28},
	"30d": {24 * time.Hour, 30},
}

// trafficSeries counts requests per bucket by outcome: served, truncated (no
// token counts), throttled (429) or refused (any other error status).
func (s *Server) trafficSeries(r *http.Request) (any, error) {
	tb, ok := trafficBuckets[r.URL.Query().Get("range")]
	if !ok {
		tb = trafficBuckets["24h"]
	}
	now := s.Now().UTC()
	start := now.Truncate(tb.bucket).Add(-time.Duration(tb.points-1) * tb.bucket)
	w := queryWindow(tb.bucket)
	if tb.bucket < time.Hour {
		w = &byMinute
	}
	rows, err := s.Client.meterQuery(r.Context(), "llm_requests", start, now, w, []string{"usage_reported", "response_code"})
	if err != nil {
		return nil, err
	}
	out := make([]model.SeriesPoint, tb.points)
	for i := range out {
		out[i].T = start.Add(time.Duration(i) * tb.bucket).UnixMilli()
	}
	for _, row := range rows {
		i := int(row.WindowStart.Sub(start) / tb.bucket)
		if i < 0 || i >= tb.points {
			continue
		}
		code, _ := strconv.Atoi(deref(row.GroupBy["response_code"]))
		n := int(row.Value)
		switch {
		case code == http.StatusTooManyRequests:
			out[i].Throttled += n
		case code >= 400:
			out[i].Blocked += n
		case deref(row.GroupBy["usage_reported"]) == "false":
			out[i].Truncated += n
		default:
			out[i].Allowed += n
		}
	}
	return out, nil
}

// spendSeries is daily spend by team over the last days (UTC days, oldest first).
func (s *Server) spendSeries(r *http.Request) (any, error) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days < 1 || days > 90 {
		days = 30
	}
	now := s.Now().UTC()
	start := now.Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	cells, err := s.usage(r.Context(), start, now, &byDay)
	if err != nil {
		return nil, err
	}
	out := make([]model.SpendPoint, days)
	for i := range out {
		out[i] = model.SpendPoint{Day: start.AddDate(0, 0, i).Format("01-02"), ByTeam: map[string]float64{}}
	}
	for _, c := range cells {
		if i := int(c.Start.Sub(start) / (24 * time.Hour)); i >= 0 && i < days {
			out[i].ByTeam[c.Team] = roundUSD(out[i].ByTeam[c.Team] + c.USD)
		}
	}
	return out, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// none answers a list this pack has nothing for.
