package api

import (
	"cmp"
	"maps"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pricing"
	"github.com/jbouder/stargate/server/internal/store"
)

// §7.5.5 savings analysis: requests a cheaper model in the same family would
// plausibly have served, and what they'd have cost on it.
//
// The method, which the console states:
//   - The last 30 days of raw receipts (all that's kept, §4.6), served
//     (200, settled) and priced. Unpriced requests have no cost to save
//     from, so they're counted apart, never as $0.
//   - "Would have served" means: the answer was short (output, reasoning
//     included, at most savingsOutputLimit tokens), and input plus output
//     fits the cheaper model's context. Quality isn't measured.
//   - The cheaper model costs the same token counts (input, cache reads and
//     writes, output, reasoning) at its price on a backend that offers it, in
//     effect when each request started; the actual cost is the receipt's.
//     A request the cheaper model has no price for isn't counted.
//   - Requests are grouped by what an alias change would move: an alias's
//     requests that ran on its target, or a key's requests for a model by
//     name. Requests a policy or fallback sent elsewhere aren't counted.
//   - Each group gets the sibling and backend that would have saved most.

// savingsOutputLimit is the longest answer counted as short: classification,
// extraction and short summaries, which a smaller model in the family
// plausibly handles. Longer answers (long-form writing, code, heavy
// reasoning) are left out.
const savingsOutputLimit = 1000

// savingsWindow is how far back the analysis reads: raw receipts' retention.
const savingsWindow = rawRetention

type SavingsView struct {
	// The window is [from, to); FirstAt is its earliest served request (0
	// when none), and Days how many days from it (or from) to to.
	From        int64   `json:"from"`
	To          int64   `json:"to"`
	FirstAt     int64   `json:"firstAt"`
	Days        float64 `json:"days"`
	OutputLimit int     `json:"outputLimit"`
	// Served counts the window's served requests; Unpriced those with no
	// price; Rerouted those that ran on a model other than the one asked
	// for (a policy, a fallback, or an alias since retargeted).
	Served        int                  `json:"served"`
	Unpriced      int                  `json:"unpriced"`
	Rerouted      int                  `json:"rerouted"`
	Opportunities []SavingsOpportunity `json:"opportunities"`
}

// SavingsOpportunity is one group's best move: its requests on Model moved
// to Target on TargetBackend. Alias is set when the requests came through
// an alias (retargeting it moves them); otherwise Key asked for Model by
// name.
type SavingsOpportunity struct {
	ID            string `json:"id"`
	Alias         string `json:"alias,omitempty"`
	Key           string `json:"key,omitempty"`
	KeyID         string `json:"keyId,omitempty"`
	Model         string `json:"model"`
	Target        string `json:"target"`
	TargetBackend string `json:"targetBackend"`
	// Over the counted requests: what they cost, what Target would have,
	// and the difference.
	Requests  int     `json:"requests"`
	ActualUSD float64 `json:"actualUsd"`
	TargetUSD float64 `json:"targetUsd"`
	SavedUSD  float64 `json:"savedUsd"`
	// Served is every served request in the group; Excluded says why the
	// rest weren't counted, each under its first reason.
	Served   int             `json:"served"`
	Excluded SavingsExcluded `json:"excluded"`
	// Keys are the counted requests' keys, by name. NotAllowedKeys don't
	// allow Target yet: moving them first needs it added, or they'd be
	// refused (403 model_not_allowed). NotAllowedRequests are theirs.
	Keys               []string `json:"keys"`
	NotAllowedKeys     []string `json:"notAllowedKeys"`
	NotAllowedRequests int      `json:"notAllowedRequests"`
}

type SavingsExcluded struct {
	Unpriced       int `json:"unpriced"`       // no price, so no cost to compare
	KeyInactive    int `json:"keyInactive"`    // the key is revoked, expired or gone
	LongOutput     int `json:"longOutput"`     // output over OutputLimit
	OverContext    int `json:"overContext"`    // input + output over Target's context
	TargetUnpriced int `json:"targetUnpriced"` // Target had no price for them
}

func (s *Server) savings(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	ctx := r.Context()
	now := time.Now().UTC()
	from := now.Add(-savingsWindow)
	g, err := s.grouper(ctx, t)
	if err != nil {
		return nil, err
	}
	aliases, err := s.Store.Aliases(ctx, t)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.PriceRowsSince(ctx, from)
	if err != nil {
		return nil, err
	}
	prices := map[[2]string][]store.PriceRow{}
	for _, p := range rows {
		k := [2]string{p.ModelID, p.Backend}
		prices[k] = append(prices[k], p)
	}
	in := savingsInput{Now: now, From: from, Periods: savingsPeriods(from, now, rows), Fits: contextThresholds(g.models),
		G: g, Prices: prices, Aliases: aliases}
	if in.Cells, err = s.Store.SavingsCells(ctx, t, from, now, in.Periods, in.Fits, savingsOutputLimit); err != nil {
		return nil, err
	}
	return analyzeSavings(in), nil
}

type savingsInput struct {
	Now, From time.Time
	// Periods start the price periods SavingsCell.Period counts (1-based).
	Periods []time.Time
	// Fits are the request-size thresholds SavingsCell.Fit counts.
	Fits    []int
	G       grouper
	Prices  map[[2]string][]store.PriceRow // per (model, backend), oldest first
	Aliases map[string]string
	Cells   []store.SavingsCell
}

// savingsPeriods splits [from, to) at every price row's start and end, so
// within a period each pair has one price.
func savingsPeriods(from, to time.Time, rows []store.PriceRow) []time.Time {
	out := []time.Time{from}
	add := func(t time.Time) {
		if t.After(from) && t.Before(to) && !slices.ContainsFunc(out, t.Equal) {
			out = append(out, t)
		}
	}
	for _, r := range rows {
		add(r.From)
		if r.To != nil {
			add(*r.To)
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

// contextThresholds are the catalog's known context lengths plus one,
// ascending: a request's width_bucket over them says which contexts it fits.
func contextThresholds(models map[string]model.Model) []int {
	var out []int
	for _, m := range models {
		if m.Context > 0 && !slices.Contains(out, m.Context+1) {
			out = append(out, m.Context+1)
		}
	}
	if len(out) == 0 {
		return []int{math.MaxInt32}
	}
	slices.Sort(out)
	return out
}

// fitBucket is width_bucket(size, th): how many thresholds size reached.
func fitBucket(th []int, size int) int {
	n := 0
	for _, x := range th {
		if size >= x {
			n++
		}
	}
	return n
}

// fitsContext reports whether every request in fit bucket b fits a context
// of ctx tokens, ctx being one of the catalog's.
func fitsContext(th []int, b, ctx int) bool {
	return b < len(th) && th[b]-1 <= ctx
}

// keyActive: a revoked or expired key's traffic won't come back.
func keyActive(k model.APIKey, now time.Time) bool {
	if k.Status == "revoked" {
		return false
	}
	return k.ExpiresAt == nil || *k.ExpiresAt >= now.Format(time.DateOnly)
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func analyzeSavings(in savingsInput) SavingsView {
	v := SavingsView{From: in.From.UnixMilli(), To: in.Now.UnixMilli(), OutputLimit: savingsOutputLimit, Opportunities: []SavingsOpportunity{}}
	type unit struct {
		alias, keyID, model string
		cells               []store.SavingsCell
	}
	units := map[string]*unit{}
	var first time.Time
	for _, c := range in.Cells {
		v.Served += c.Requests
		if !c.Priced {
			v.Unpriced += c.Requests
		}
		if first.IsZero() || c.First.Before(first) {
			first = c.First
		}
		var id string
		u := unit{model: c.Resolved}
		if a, ok := store.MatchAlias(in.Aliases, c.Requested); ok {
			if in.Aliases[a] != c.Resolved {
				v.Rerouted += c.Requests
				continue
			}
			id, u.alias = "alias:"+a+":"+c.Resolved, a
		} else if c.Requested == c.Resolved {
			id, u.keyID = "key:"+c.KeyID+":"+c.Resolved, c.KeyID
		} else {
			v.Rerouted += c.Requests
			continue
		}
		if units[id] == nil {
			units[id] = &u
		}
		units[id].cells = append(units[id].cells, c)
	}
	if !first.IsZero() {
		v.FirstAt = first.UnixMilli()
		v.Days = math.Round(in.Now.Sub(maxTime(first, in.From)).Hours()/24*10) / 10
	} else {
		v.Days = math.Round(in.Now.Sub(in.From).Hours()/24*10) / 10
	}

	models := slices.Sorted(maps.Keys(in.G.models))
	backends := slices.Sorted(maps.Keys(in.G.backends))
	for id, u := range units {
		m, ok := in.G.models[u.model]
		if !ok {
			continue
		}
		var best *SavingsOpportunity
		for _, cid := range models {
			c := in.G.models[cid]
			if cid == u.model || c.Family != m.Family || c.Context <= 0 {
				continue
			}
			for _, b := range backends {
				if !slices.Contains(in.G.backends[b].Models, cid) {
					continue
				}
				o := in.evaluate(u.cells, c, b)
				if o.Requests > 0 && o.SavedUSD > 0 && (best == nil || o.SavedUSD > best.SavedUSD) {
					best = &o
				}
			}
		}
		if best == nil {
			continue
		}
		best.ID, best.Alias, best.KeyID, best.Model = id, u.alias, u.keyID, u.model
		if u.keyID != "" {
			best.Key = u.keyID
			if k, ok := in.G.keys[u.keyID]; ok {
				best.Key = k.Name
			}
		}
		v.Opportunities = append(v.Opportunities, *best)
	}
	slices.SortFunc(v.Opportunities, func(a, b SavingsOpportunity) int {
		return cmp.Or(cmp.Compare(b.SavedUSD, a.SavedUSD), cmp.Compare(a.ID, b.ID))
	})
	return v
}

// evaluate prices one group's requests on target at backend.
func (in savingsInput) evaluate(cells []store.SavingsCell, target model.Model, backend string) SavingsOpportunity {
	o := SavingsOpportunity{Target: target.ID, TargetBackend: backend, Keys: []string{}, NotAllowedKeys: []string{}}
	rows := in.Prices[[2]string{target.ID, backend}]
	for _, c := range cells {
		n := c.Requests
		o.Served += n
		k, known := in.G.keys[c.KeyID]
		switch {
		case !c.Priced:
			o.Excluded.Unpriced += n
			continue
		case !known || !keyActive(k, in.Now):
			o.Excluded.KeyInactive += n
			continue
		case !c.Short:
			o.Excluded.LongOutput += n
			continue
		case !fitsContext(in.Fits, c.Fit, target.Context):
			o.Excluded.OverContext += n
			continue
		}
		var cost *float64
		if c.Period >= 1 && c.Period <= len(in.Periods) {
			if row, ok := store.PriceAt(rows, in.Periods[c.Period-1]); ok {
				cost = pricing.Cost(row.Rates, c.Tokens)
			}
		}
		if cost == nil {
			o.Excluded.TargetUnpriced += n
			continue
		}
		o.Requests += n
		o.ActualUSD += c.USD
		o.TargetUSD += *cost
		if !slices.Contains(o.Keys, k.Name) {
			o.Keys = append(o.Keys, k.Name)
		}
		if !slices.Contains(k.AllowedModels, target.ID) {
			o.NotAllowedRequests += n
			if !slices.Contains(o.NotAllowedKeys, k.Name) {
				o.NotAllowedKeys = append(o.NotAllowedKeys, k.Name)
			}
		}
	}
	slices.Sort(o.Keys)
	slices.Sort(o.NotAllowedKeys)
	o.SavedUSD = round6(o.ActualUSD - o.TargetUSD)
	o.ActualUSD, o.TargetUSD = round6(o.ActualUSD), round6(o.TargetUSD)
	return o
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
