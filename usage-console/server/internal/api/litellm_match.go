package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jbouder/stargate/server/internal/pricing"
)

// Setup links each model a provider adds to its LiteLLM entry, so its price,
// context, modalities and retirement date are there from the start rather
// than "No price" and "Unknown" until someone links it on Models. The form
// asks GET /pricing/litellm/match for what to offer; creating the backend
// with litellmKeys links the kept ones and syncs once.

// LiteLLMMatch is a model's entry for a provider, or none (LiteLLMKey "").
type LiteLLMMatch struct {
	Model      string         `json:"model"`
	LiteLLMKey string         `json:"litellmKey"`
	Rates      map[string]any `json:"rates,omitempty"` // per 1M tokens, by rate
	Context    int            `json:"context,omitempty"`
	Modalities []string       `json:"modalities,omitempty"`
}

// liteLLMFile is LiteLLM's file read for matching: prices and facts.
func (s *Server) liteLLMFile(ctx context.Context) (map[string]pricing.Rates, map[string]pricing.Facts, error) {
	raw, err := s.fetchLiteLLMFile(ctx)
	if err != nil {
		return nil, nil, err
	}
	rates, err := pricing.ParseLiteLLM(raw)
	if err != nil {
		return nil, nil, err
	}
	facts, err := pricing.ParseFacts(raw)
	return rates, facts, err
}

// liteLLMMatch is GET /pricing/litellm/match?provider=Anthropic&models=a,b.
func (s *Server) liteLLMMatch(_ http.ResponseWriter, r *http.Request, _ string) (any, error) {
	q := r.URL.Query()
	var models []string
	for _, m := range strings.Split(q.Get("models"), ",") {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(models, m) {
			models = append(models, m)
		}
	}
	rates, facts, err := s.liteLLMFile(r.Context())
	if err != nil {
		return nil, unavailable("couldn't load LiteLLM's price file: " + err.Error())
	}
	out := []LiteLLMMatch{}
	for _, m := range models {
		x := LiteLLMMatch{Model: m, LiteLLMKey: pricing.Match(facts, rates, q.Get("provider"), m)}
		if x.LiteLLMKey != "" {
			f := facts[x.LiteLLMKey]
			x.Context, x.Modalities, x.Rates = f.Context, f.Modalities, rateMap(rates[x.LiteLLMKey])
		}
		out = append(out, x)
	}
	return out, nil
}

// rateMap is rates by name, per 1M tokens, as GET /pricing names them.
func rateMap(r pricing.Rates) map[string]any {
	out := map[string]any{}
	for i, v := range r {
		if v != nil {
			out[pricing.Names[i]] = *v
		}
	}
	return out
}

// checkLiteLLMKeys refuses keys for models the backend doesn't serve, or
// that LiteLLM's file doesn't price.
func (s *Server) checkLiteLLMKeys(ctx context.Context, served []string, keys map[string]string) error {
	if len(keys) == 0 {
		return nil
	}
	rates, _, err := s.liteLLMFile(ctx)
	if err != nil {
		return unavailable("couldn't load LiteLLM's price file to check the keys: " + err.Error())
	}
	for m, k := range keys {
		if !slices.Contains(served, m) {
			return badRequest(fmt.Sprintf("litellmKeys names %s, which this provider doesn't serve", m))
		}
		if _, ok := rates[k]; !ok {
			return badRequest(fmt.Sprintf("LiteLLM's price file has no token prices under %q", k))
		}
	}
	return nil
}
