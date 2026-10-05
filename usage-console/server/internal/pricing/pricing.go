// Package pricing holds the pure rules for pricing a (model, backend) pair:
// what a receipt costs at a set of rates, how LiteLLM's price file reads, and
// what a sync does to each rate (decisions §1). The store applies the plans.
package pricing

import (
	"encoding/json"
	"errors"
	"math"
)

// Rate is one token type's price.
type Rate int

const (
	Input Rate = iota
	CachedInput
	CacheWrite
	Output
	Reasoning
	NumRates
)

// Names are the rates' JSON keys; Labels are how the console and audit log
// say them.
var (
	Names  = [NumRates]string{"input", "cachedInput", "cacheWrite", "output", "reasoning"}
	Labels = [NumRates]string{"Input", "Cached input", "Cache write", "Output", "Reasoning"}
)

// Source is where a rate came from. "" goes with a missing rate.
type Source string

const (
	Seed    Source = "seed"
	LiteLLM Source = "litellm"
	Manual  Source = "manual"
)

// Rates are dollars per 1M tokens, indexed by Rate. nil is no price.
type Rates [NumRates]*float64

// Sources say where each of a row's rates came from.
type Sources [NumRates]Source

// Seen is the last value LiteLLM gave for each rate, which is what decides
// whether it has moved since an override was set.
type Seen [NumRates]*float64

// Priced reports whether every rate is set.
func (r Rates) Priced() bool {
	for _, v := range r {
		if v == nil {
			return false
		}
	}
	return true
}

// Tokens are a receipt's counts. Cached (cache reads) and CacheWrite are
// part of Input, as in OpenAI's usage.
type Tokens struct{ Input, Cached, CacheWrite, Output, Reasoning int }

// Cost is what tokens cost at r, or nil when a rate they need is missing:
// an unpriced request has no cost rather than $0.
func Cost(r Rates, t Tokens) *float64 {
	n := [NumRates]int{max(t.Input-t.Cached-t.CacheWrite, 0), t.Cached, t.CacheWrite, t.Output, t.Reasoning}
	if !r.Priced() && n == ([NumRates]int{}) {
		return nil
	}
	sum := 0.0
	for i, k := range n {
		if k == 0 {
			continue
		}
		if r[i] == nil {
			return nil
		}
		sum += float64(k) * *r[i]
	}
	sum /= 1_000_000
	return &sum
}

// round6 keeps a per-1M rate to model_pricing's 6 decimal places, so float
// noise in the file (1.500002e-05) isn't a price change every day.
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// ParseLiteLLM reads model_prices_and_context_window.json into rates per
// key. Entries without both an input and an output token price (images,
// embeddings, the sample spec) are left out. A missing cache-read or
// cache-write rate falls back to the input rate, and a missing reasoning
// rate to the output rate, which is how those tokens bill when a provider
// has no separate price.
func ParseLiteLLM(b []byte) (map[string]Rates, error) {
	var file map[string]json.RawMessage
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, err
	}
	out := map[string]Rates{}
	for key, raw := range file {
		var e struct {
			In        *float64 `json:"input_cost_per_token"`
			Out       *float64 `json:"output_cost_per_token"`
			Read      *float64 `json:"cache_read_input_token_cost"`
			Write     *float64 `json:"cache_creation_input_token_cost"`
			Reasoning *float64 `json:"output_cost_per_reasoning_token"`
		}
		if json.Unmarshal(raw, &e) != nil || e.In == nil || e.Out == nil {
			continue
		}
		or := func(v, fallback *float64) *float64 {
			if v == nil {
				v = fallback
			}
			x := round6(*v * 1e6)
			return &x
		}
		out[key] = Rates{or(e.In, nil), or(e.Read, e.In), or(e.Write, e.In), or(e.Out, nil), or(e.Reasoning, e.Out)}
	}
	if len(out) == 0 {
		return nil, errors.New("no token prices in the file")
	}
	return out, nil
}

// Current is a pair's row in effect now; the zero value is no row.
type Current struct {
	Rates   Rates
	Sources Sources
}

// Proposal is a LiteLLM move on an overridden rate, for someone to accept
// or dismiss.
type Proposal struct {
	Rate              Rate
	Current, Proposed float64
}

// Plan is what a sync does to one pair.
type Plan struct {
	Rates     Rates
	Sources   Sources
	Changed   bool // Rates or Sources differ from the current row
	Proposals []Proposal
	Seen      Seen // LiteLLM's values to remember
}

// Same reports whether two rates are equal at model_pricing's precision (6
// decimal places), so a value read back from the database matches the one
// that was written.
func Same(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || math.Round(*a*1e6) == math.Round(*b*1e6))
}

// PlanSync decides each rate of a pair from LiteLLM's entry (nil when the
// pair has no LiteLLM key, or the file lacks it):
//   - a manual rate stays; if LiteLLM moved since it was last seen, and not
//     onto the override, that's a proposal
//   - any other rate takes LiteLLM's value
//   - with no LiteLLM value, seed rates end (no price) and the rest stay
func PlanSync(cur Current, lite *Rates, seen Seen) Plan {
	p := Plan{Rates: cur.Rates, Sources: cur.Sources, Seen: seen}
	for i := range NumRates {
		if lite == nil {
			if cur.Sources[i] == Seed {
				p.Rates[i], p.Sources[i] = nil, ""
			}
			continue
		}
		v := lite[i]
		if cur.Sources[i] == Manual {
			if seen[i] != nil && !Same(seen[i], v) && !Same(cur.Rates[i], v) {
				p.Proposals = append(p.Proposals, Proposal{Rate: i, Current: *cur.Rates[i], Proposed: *v})
			}
		} else {
			p.Rates[i], p.Sources[i] = v, LiteLLM
		}
		p.Seen[i] = v
	}
	for i := range NumRates {
		if !Same(p.Rates[i], cur.Rates[i]) || p.Sources[i] != cur.Sources[i] {
			p.Changed = true
		}
	}
	return p
}
