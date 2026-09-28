package api

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func (s *Server) aliases(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	aliases, err := s.Store.Aliases(r.Context())
	if err != nil {
		return nil, err
	}
	requested, err := s.Store.RequestedModels(r.Context(), t, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	return aliasViews(aliases, requested), nil
}

func (s *Server) pricing(_ http.ResponseWriter, r *http.Request, _ string) (any, error) {
	rows, err := s.Store.PriceRows(r.Context())
	if err != nil {
		return nil, err
	}
	return pricingView(rows, time.Now()), nil
}

// aliasViews credits each requested model's count to the alias the gateway
// would resolve it through, so requests later rerouted still count.
func aliasViews(aliases map[string]string, requested map[string]int) []model.Alias {
	byAlias := map[string]int{}
	for m, n := range requested {
		if a, ok := store.MatchAlias(aliases, m); ok {
			byAlias[a] += n
		}
	}
	out := make([]model.Alias, 0, len(aliases))
	for a, target := range aliases {
		out = append(out, model.Alias{Alias: a, Target: target, Requests24h: byAlias[a]})
	}
	slices.SortFunc(out, func(x, y model.Alias) int { return cmp.Compare(x.Alias, y.Alias) })
	return out
}

var priceFields = []struct {
	name string
	of   func(store.PriceRow) float64
}{
	{"Input", func(p store.PriceRow) float64 { return p.InPerM }},
	{"Cached input", func(p store.PriceRow) float64 { return p.CachedPerM }},
	{"Output", func(p store.PriceRow) float64 { return p.OutPerM }},
	{"Reasoning", func(p store.PriceRow) float64 { return p.ReasoningPerM }},
}

// pricingView takes rows sorted by model then effective_from. A change is a
// rate that differs from the model's previous row, scheduled ones included.
func pricingView(rows []store.PriceRow, now time.Time) model.Pricing {
	out := model.Pricing{EffectiveFrom: map[string]string{}, Changes: []model.PriceChange{}}
	for i, p := range rows {
		if !p.From.After(now) && (p.To == nil || p.To.After(now)) {
			out.EffectiveFrom[p.ModelID] = p.From.UTC().Format(time.DateOnly)
		}
		if i == 0 || rows[i-1].ModelID != p.ModelID {
			continue
		}
		for _, f := range priceFields {
			if was, is := f.of(rows[i-1]), f.of(p); was != is {
				out.Changes = append(out.Changes, model.PriceChange{
					Model: p.ModelID, Field: f.name, From: was, To: is, Effective: p.From.UTC().Format(time.DateOnly),
				})
			}
		}
	}
	slices.SortStableFunc(out.Changes, func(x, y model.PriceChange) int { return cmp.Compare(y.Effective, x.Effective) })
	return out
}
