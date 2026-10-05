package api

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/jbouder/stargate/server/internal/store"
)

// rangeDuration is the span a range= value names.
func rangeDuration(r *http.Request) (string, time.Duration) {
	name := r.URL.Query().Get("range")
	rb, ok := rangeBuckets[name]
	if !ok {
		name, rb = "24h", rangeBuckets["24h"]
	}
	return name, rb.bucket * time.Duration(rb.points)
}

// Summary is the Overview's three numbers for a range, the same span before
// it for the deltas, and what's driving spend.
type Summary struct {
	Range    string             `json:"range"`
	From     int64              `json:"from"`
	To       int64              `json:"to"`
	Current  store.WindowTotals `json:"current"`
	Previous store.WindowTotals `json:"previous"`
	// TopTeamIncrease is the team whose spend rose most against the previous
	// window, or nil when none rose.
	TopTeamIncrease *TeamIncrease `json:"topTeamIncrease"`
	KeyAnomalies    []KeyAnomaly  `json:"keyAnomalies"`
}

type TeamIncrease struct {
	Team     string  `json:"team"`
	DeltaUSD float64 `json:"deltaUsd"`
}

// KeyAnomaly is a key whose last 24 hours cost well over its daily average
// for the 7 days before.
type KeyAnomaly struct {
	KeyID       string  `json:"keyId"`
	KeyName     string  `json:"keyName"`
	SpendUSD    float64 `json:"spendUsd"`
	BaselineUSD float64 `json:"baselineUsd"`
	Ratio       float64 `json:"ratio"`
	// TopModel took the largest share of the key's last-24h spend.
	TopModel      string  `json:"topModel"`
	TopModelShare float64 `json:"topModelShare"`
}

const (
	anomalyRatio = 2.0
	anomalyMinUS = 1.0 // ignore keys spending pennies
)

func (s *Server) summary(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	name, span := rangeDuration(r)
	now := time.Now()
	out := Summary{Range: name, From: now.Add(-span).UnixMilli(), To: now.UnixMilli(), KeyAnomalies: []KeyAnomaly{}}
	var err error
	if out.Current, err = s.Store.Totals(ctx, t, now.Add(-span), now); err != nil {
		return nil, err
	}
	if out.Previous, err = s.Store.Totals(ctx, t, now.Add(-2*span), now.Add(-span)); err != nil {
		return nil, err
	}
	cur, err := s.Store.SpendBy(ctx, t, now.Add(-span), now)
	if err != nil {
		return nil, err
	}
	prev, err := s.Store.SpendBy(ctx, t, now.Add(-2*span), now.Add(-span))
	if err != nil {
		return nil, err
	}
	for team, usd := range cur.Team {
		if d := usd - prev.Team[team]; d > 0 && (out.TopTeamIncrease == nil || d > out.TopTeamIncrease.DeltaUSD) {
			out.TopTeamIncrease = &TeamIncrease{Team: team, DeltaUSD: d}
		}
	}

	// Anomalies always compare the last day with the week before it.
	day, err := s.Store.SpendBy(ctx, t, now.Add(-24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	week, err := s.Store.SpendBy(ctx, t, now.Add(-8*24*time.Hour), now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	keys, err := s.Store.Keys(ctx, t)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, k := range keys {
		names[k.ID] = k.Name
	}
	for id, usd := range day.Key {
		base := week.Key[id] / 7
		if usd < anomalyMinUS || base <= 0 || usd < anomalyRatio*base {
			continue
		}
		a := KeyAnomaly{KeyID: id, KeyName: cmp.Or(names[id], id), SpendUSD: usd, BaselineUSD: base, Ratio: usd / base}
		for km, v := range day.KeyModel {
			if km[0] == id && v > a.TopModelShare*usd {
				a.TopModel, a.TopModelShare = km[1], v/usd
			}
		}
		out.KeyAnomalies = append(out.KeyAnomalies, a)
	}
	slices.SortFunc(out.KeyAnomalies, func(a, b KeyAnomaly) int { return cmp.Compare(b.Ratio, a.Ratio) })
	return out, nil
}

// ChangeImpact compares traffic in equal windows either side of a change.
type ChangeImpact struct {
	ChangeID      string       `json:"changeId"`
	TS            int64        `json:"ts"`
	WindowMinutes int          `json:"windowMinutes"`
	Before        store.Impact `json:"before"`
	After         store.Impact `json:"after"`
}

func (s *Server) changeImpact(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	changes, err := s.Store.Changes(ctx, t, 500, "")
	if err != nil {
		return nil, err
	}
	var at time.Time
	id := r.PathValue("id")
	found := false
	for _, c := range changes {
		if c.ID == id {
			at, found = time.UnixMilli(c.TS), true
			break
		}
	}
	if !found {
		return nil, store.ErrNotFound
	}
	// The after window can't run past now; the before window matches it.
	w := time.Duration(intParam(r, "minutes", 40, 5, 24*60)) * time.Minute
	if left := time.Since(at); left < w {
		w = left
	}
	out := ChangeImpact{ChangeID: id, TS: at.UnixMilli(), WindowMinutes: int(w.Minutes())}
	if out.Before, err = s.Store.Impact(ctx, t, at.Add(-w), at); err != nil {
		return nil, err
	}
	if out.After, err = s.Store.Impact(ctx, t, at, at.Add(w)); err != nil {
		return nil, err
	}
	return out, nil
}
