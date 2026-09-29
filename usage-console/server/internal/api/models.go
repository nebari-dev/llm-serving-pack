package api

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func (s *Server) aliases(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	aliases, err := s.Store.Aliases(r.Context(), t)
	if err != nil {
		return nil, err
	}
	requested, err := s.Store.RequestedModels(r.Context(), t, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	return aliasViews(aliases, requested), nil
}

// putAlias takes {"target": model}: it creates the alias in the path, or
// points it at a new target. If-Match, when sent, must be its version.
func (s *Server) putAlias(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	alias := r.PathValue("alias")
	models, err := s.Store.Models(r.Context())
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	if err := store.ValidateAlias(alias, in.Target, ids); err != nil {
		return nil, badRequest(err.Error())
	}
	if err := s.Store.PutAlias(r.Context(), t, s.DevActor, alias, in.Target, r.Header.Get("If-Match")); err != nil {
		return nil, err
	}
	s.configChanged()
	return s.aliasView(w, r, t, alias)
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request, t string) (any, error) {
	alias := r.PathValue("alias")
	if err := s.Store.DeleteAlias(r.Context(), t, s.DevActor, alias, r.Header.Get("If-Match")); err != nil {
		return nil, err
	}
	s.configChanged()
	return map[string]string{"alias": alias}, nil
}

// aliasView is one alias as GET /aliases shows it, with its ETag set.
func (s *Server) aliasView(w http.ResponseWriter, r *http.Request, t, alias string) (any, error) {
	all, err := s.aliases(w, r, t)
	if err != nil {
		return nil, err
	}
	for _, a := range all.([]model.Alias) {
		if a.Alias == alias {
			w.Header().Set("ETag", a.ETag)
			return a, nil
		}
	}
	return nil, store.ErrNotFound
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
		out = append(out, model.Alias{Alias: a, Target: target, Requests24h: byAlias[a], ETag: store.ETag(store.AliasRow{Alias: a, Target: target})})
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
					EffectiveAt: p.From.UnixMilli(), Scheduled: p.From.After(now),
				})
			}
		}
	}
	slices.SortStableFunc(out.Changes, func(x, y model.PriceChange) int { return cmp.Compare(y.Effective, x.Effective) })
	return out
}

// setPrice takes {inPerM?, outPerM?, cachedPerM?, reasoningPerM?,
// effectiveFrom?}: the model's next price, from effectiveFrom (RFC 3339, now
// if omitted). Rates left out keep their current value.
func (s *Server) setPrice(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	var in struct {
		InPerM, OutPerM, CachedPerM, ReasoningPerM *float64
		EffectiveFrom                              string `json:"effectiveFrom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	id := r.PathValue("model")
	models, err := s.Store.Models(r.Context())
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(models, func(m model.Model) bool { return m.ID == id })
	if i < 0 {
		return nil, store.ErrNotFound
	}
	now := time.Now()
	cur := models[i]
	p := store.PriceRow{ModelID: id, InPerM: cur.InPerM, OutPerM: cur.OutPerM, CachedPerM: cur.CachedPerM, ReasoningPerM: cur.ReasoningPerM, From: now}
	for _, f := range []struct {
		in  *float64
		out *float64
	}{{in.InPerM, &p.InPerM}, {in.OutPerM, &p.OutPerM}, {in.CachedPerM, &p.CachedPerM}, {in.ReasoningPerM, &p.ReasoningPerM}} {
		if f.in != nil {
			*f.out = *f.in
		}
	}
	if in.EffectiveFrom != "" {
		if p.From, err = time.Parse(time.RFC3339, in.EffectiveFrom); err != nil {
			return nil, badRequest("effectiveFrom must be an RFC 3339 time")
		}
	}
	if err := s.Store.SetPrice(r.Context(), t, s.DevActor, p, now); store.IsBadPrice(err) {
		return nil, badRequest(err.Error())
	} else if err != nil {
		return nil, err
	}
	s.configChanged()
	return s.pricing(nil, r, t)
}

// cancelPrice drops a scheduled price change: DELETE /pricing/{model}/{at},
// with at its effectiveAt (epoch ms).
func (s *Server) cancelPrice(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ms, err := strconv.ParseInt(r.PathValue("at"), 10, 64)
	if err != nil {
		return nil, badRequest("at must be the change's effectiveAt (epoch ms)")
	}
	err = s.Store.CancelPrice(r.Context(), t, s.DevActor, r.PathValue("model"), time.UnixMilli(ms), time.Now())
	if store.IsBadPrice(err) {
		return nil, badRequest(err.Error())
	} else if err != nil {
		return nil, err
	}
	s.configChanged()
	return s.pricing(nil, r, t)
}
