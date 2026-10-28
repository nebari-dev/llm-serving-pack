package openmeter

import (
	"context"
	"fmt"
	"log"
	"maps"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/shared/openmeterapi"
)

// BootstrapConfig is what the pack sets up in OpenMeter: a priced feature
// per model resource, and prices for models no public price list has.
type BootstrapConfig struct {
	Resources []Resource    `json:"resources"`
	Prices    []ManualPrice `json:"prices"`
}

// Resource is a model resource of the LLM serving pack and the OpenMeter
// provider its models are priced under (bedrock, openai, or one of the
// manual prices' providers).
type Resource struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// ManualPrice is a price override: per-token USD rates keyed as OpenMeter's
// pricing (input_per_token, output_per_token, cache_read_per_token, ...).
type ManualPrice struct {
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Pricing  map[string]string `json:"pricing"`
}

// tokensMeter is the meter features price: the collector's llm.tokens events.
const tokensMeter = "llm_tokens"

// Bootstrap creates what cfg lists and OpenMeter doesn't have yet. A feature
// is matched by key, a price by provider, model and rates.
func Bootstrap(ctx context.Context, c *Client, cfg BootstrapConfig, now func() time.Time) error {
	type meter struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	meters, err := openmeterapi.ListAll[meter](ctx, c.Client, "/api/v3/openmeter/meters", nil)
	if err != nil {
		return err
	}
	meterID := ""
	for _, m := range meters {
		if m.Key == tokensMeter {
			meterID = m.ID
		}
	}
	if meterID == "" {
		return fmt.Errorf("openmeter has no %s meter", tokensMeter)
	}

	fs, err := c.Features(ctx)
	if err != nil {
		return err
	}
	have := map[string][]Feature{}
	for _, f := range fs {
		r := f.Meter.Filters["model_resource"]["eq"]
		have[r] = append(have[r], f)
	}
	for _, r := range cfg.Resources {
		want := llmUnitCost(r.Provider)
		for _, f := range have[r.Name] {
			if f.UnitCost == want {
				continue
			}
			// OpenMeter prices a feature's usage when it is queried, from the
			// feature's unit cost as it is then, so this also re-prices the
			// usage the resource recorded under the old provider.
			log.Printf("feature %s (%s): unit cost %+v differs from the values' %+v; updating it, which re-prices this resource's past usage too",
				f.Key, r.Name, f.UnitCost, want)
			if err := c.Do(ctx, http.MethodPatch, "/api/v3/openmeter/features/"+url.PathEscape(f.ID), nil, map[string]any{"unit_cost": want}, nil); err != nil {
				return fmt.Errorf("feature for %s: %w", r.Name, err)
			}
		}
		if len(have[r.Name]) > 0 {
			continue
		}
		err := c.Do(ctx, http.MethodPost, "/api/v3/openmeter/features", nil, map[string]any{
			"key":  featureKey(r.Name),
			"name": r.Name + " tokens",
			"meter": map[string]any{
				"id":      meterID,
				"filters": map[string]any{"model_resource": map[string]string{"eq": r.Name}},
			},
			"unit_cost": want,
		}, nil)
		if err != nil {
			return fmt.Errorf("feature for %s: %w", r.Name, err)
		}
	}

	ov, err := c.Overrides(ctx)
	if err != nil {
		return err
	}
	for _, p := range cfg.Prices {
		if hasPrice(ov, p) {
			continue
		}
		err := c.Do(ctx, http.MethodPost, "/api/v3/openmeter/llm-cost/overrides", nil, map[string]any{
			"provider": p.Provider, "model_id": p.Model, "pricing": p.Pricing, "currency": "USD",
			"effective_from": now().UTC().Format(time.RFC3339),
		}, nil)
		if err != nil {
			return fmt.Errorf("price for %s/%s: %w", p.Provider, p.Model, err)
		}
	}
	return nil
}

// hasPrice reports whether ov has p. OpenMeter stores rates as
// decimals and lists them in canonical form (0.000003 for 3e-6, "0" for a
// rate never set), so rates are compared as numbers, a missing one as zero.
func hasPrice(ov []Price, p ManualPrice) bool {
	for _, o := range ov {
		if o.Provider.ID == p.Provider && o.Model.ID == p.Model && samePricing(o.Pricing, p.Pricing) {
			return true
		}
	}
	return false
}

func samePricing(a, b map[string]string) bool {
	for k := range maps.Keys(a) {
		if !sameRate(a[k], b[k]) {
			return false
		}
	}
	for k := range maps.Keys(b) {
		if !sameRate(a[k], b[k]) {
			return false
		}
	}
	return true
}

// sameRate compares two decimal rates; "" is zero, and a rate that does not
// parse equals only the same string.
func sameRate(a, b string) bool {
	ra, okA := rate(a)
	rb, okB := rate(b)
	if !okA || !okB {
		return a == b
	}
	return ra.Cmp(rb) == 0
}

func rate(s string) (*big.Rat, bool) {
	if s == "" {
		return new(big.Rat), true
	}
	return new(big.Rat).SetString(s)
}

var notKeyChar = regexp.MustCompile(`[^a-z0-9]+`)

// featureKey is a feature key for a model resource: mock-provider -> mock_provider_tokens.
func featureKey(resource string) string {
	return notKeyChar.ReplaceAllString(resource, "_") + "_tokens"
}

// llmUnitCost is the unit cost of a model resource's feature: tokens priced
// from OpenMeter's LLM prices for the provider, by the events' model and
// token type.
func llmUnitCost(provider string) UnitCost {
	return UnitCost{Type: "llm", Provider: provider, ModelProperty: "model", TokenTypeProperty: "token_type"}
}
