package openmeter

import (
	"net/http"
)

// summary is the Overview's totals for the range and the span before it.
func (s *Server) summary(r *http.Request) (any, error) {
	ctx := r.Context()
	name, span := rangeOf(r)
	now := s.Now().UTC()
	from := now.Add(-span)
	out := Summary{Range: name, From: from.UnixMilli(), To: now.UnixMilli()}
	cur, err := s.usage(ctx, from, now, nil)
	if err != nil {
		return nil, err
	}
	prev, err := s.usage(ctx, from.Add(-span), from, nil)
	if err != nil {
		return nil, err
	}
	out.Current, out.Previous = totals(cur), totals(prev)

	delta := map[string]float64{}
	for _, c := range cur {
		delta[c.Team] += c.USD
	}
	for _, c := range prev {
		delta[c.Team] -= c.USD
	}
	for team, d := range delta {
		d = roundUSD(d)
		if d > 0 && (out.TopTeamIncrease == nil || d > out.TopTeamIncrease.DeltaUSD) {
			out.TopTeamIncrease = &TeamIncrease{Team: team, DeltaUSD: d}
		}
	}
	return out, nil
}

func totals(cells []cell) WindowTotals {
	var w WindowTotals
	for _, c := range cells {
		w.Requests += c.Requests
		w.SpendUSD += c.USD
		if c.Unpriced {
			w.Unpriced += c.Requests
		}
	}
	w.SpendUSD = roundUSD(w.SpendUSD)
	return w
}
