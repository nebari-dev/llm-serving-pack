package openmeter

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Price is one OpenMeter LLM price row: a manual override or a synced price.
type Price struct {
	Provider struct {
		ID string `json:"id"`
	} `json:"provider"`
	Model struct {
		ID string `json:"id"`
	} `json:"model"`
	EffectiveFrom time.Time         `json:"effective_from"`
	EffectiveTo   *time.Time        `json:"effective_to"`
	Pricing       map[string]string `json:"pricing"`
}

// Feature is a priced OpenMeter feature; the pack makes one per model
// resource, over the llm_tokens meter filtered to that resource.
type Feature struct {
	ID    string `json:"id"`
	Meter struct {
		Filters map[string]map[string]string `json:"filters"`
	} `json:"meter"`
	UnitCost *struct {
		Type     string `json:"type"`
		Provider string `json:"provider"`
	} `json:"unit_cost"`
}

func (c *Client) Features(ctx context.Context) ([]Feature, error) {
	return listAll[Feature](ctx, c, "/api/v3/openmeter/features", nil)
}

func (c *Client) Overrides(ctx context.Context) ([]Price, error) {
	return listAll[Price](ctx, c, "/api/v3/openmeter/llm-cost/overrides", nil)
}

func (c *Client) Prices(ctx context.Context, provider, model string) ([]Price, error) {
	return listAll[Price](ctx, c, "/api/v3/openmeter/llm-cost/prices", url.Values{"filter[provider][eq]": {provider}, "filter[model_id][eq]": {model}})
}

// tokenRate names each collector token type's rate in OpenMeter's pricing.
var tokenRate = map[string]string{
	"input":       "input_per_token",
	"output":      "output_per_token",
	"cache_read":  "cache_read_per_token",
	"cache_write": "cache_write_per_token",
	"reasoning":   "reasoning_per_token",
}

// pricer costs requests as OpenMeter's cost queries do: per model resource's
// provider, the override in effect now, else the newest synced price in
// effect now. Now, not at each request's time: OpenMeter resolves one price
// per model for a whole cost query at the current time (ResolvePrice with no
// At, openmeter/cost/adapter/adapter.go at the pinned commit), and a new
// override soft-deletes the old one, so Spend re-costs past usage at today's
// price too. Pricing receipts at their own time would stop them adding up to
// Spend.
type pricer struct {
	client    *Client
	now       time.Time
	providers map[string]string // model resource -> provider
	overrides []Price
	// mu guards cache: a cached pricer is shared by concurrent requests.
	mu    sync.Mutex
	cache map[[2]string]*Price
}

// pricerTTL is how long a pricer's features, overrides and prices are
// reused before they are read from OpenMeter again.
const pricerTTL = time.Minute

// pricer returns a pricer read from OpenMeter at most pricerTTL ago, so
// list and stream reads don't page every feature and override each time.
func (s *Server) pricer(ctx context.Context) (*pricer, error) {
	s.pricerMu.Lock()
	defer s.pricerMu.Unlock()
	if s.cachedPricer != nil && s.Now().Sub(s.cachedPricer.now) < pricerTTL {
		return s.cachedPricer, nil
	}
	p, err := s.newPricer(ctx)
	if err != nil {
		return nil, err
	}
	s.cachedPricer = p
	return p, nil
}

func (s *Server) newPricer(ctx context.Context) (*pricer, error) {
	fs, err := s.Client.Features(ctx)
	if err != nil {
		return nil, err
	}
	ov, err := s.Client.Overrides(ctx)
	if err != nil {
		return nil, err
	}
	return &pricer{client: s.Client, now: s.Now(), providers: providersOf(fs), overrides: ov, cache: map[[2]string]*Price{}}, nil
}

// providersOf maps each model resource to its price feature's provider.
func providersOf(fs []Feature) map[string]string {
	out := map[string]string{}
	for _, f := range fs {
		if f.UnitCost == nil || f.UnitCost.Type != "llm" {
			continue
		}
		if r := f.Meter.Filters["model_resource"]["eq"]; r != "" {
			out[r] = f.UnitCost.Provider
		}
	}
	return out
}

// cost is the request's cost in USD, or nil when its model has no price or
// one of its token types has no rate.
func (p *pricer) cost(ctx context.Context, resource, model string, tokens map[string]int) (*float64, error) {
	pr, err := p.price(ctx, p.providers[resource], model)
	if err != nil || pr == nil {
		return nil, err
	}
	total := 0.0
	for typ, n := range tokens {
		if n == 0 {
			continue
		}
		v, ok := pr.Pricing[tokenRate[typ]]
		if !ok {
			return nil, nil
		}
		rate, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, nil
		}
		total += float64(n) * rate
	}
	return &total, nil
}

func (p *pricer) price(ctx context.Context, provider, model string) (*Price, error) {
	if provider == "" {
		return nil, nil
	}
	provider, model = strings.ToLower(strings.TrimSpace(provider)), canonicalModel(model)
	k := [2]string{provider, model}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pr, ok := p.cache[k]; ok {
		return pr, nil
	}
	pr := p.inEffect(p.overrides, provider, model)
	if pr == nil {
		rows, err := p.client.Prices(ctx, provider, model)
		if err != nil {
			return nil, err
		}
		pr = p.inEffect(rows, provider, model)
	}
	p.cache[k] = pr
	return pr, nil
}

func (p *pricer) inEffect(rows []Price, provider, model string) *Price {
	var best *Price
	for i := range rows {
		r := &rows[i]
		if r.Provider.ID != provider || r.Model.ID != model || r.EffectiveFrom.After(p.now) {
			continue
		}
		if r.EffectiveTo != nil && !r.EffectiveTo.After(p.now) {
			continue
		}
		if best == nil || r.EffectiveFrom.After(best.EffectiveFrom) {
			best = r
		}
	}
	return best
}
