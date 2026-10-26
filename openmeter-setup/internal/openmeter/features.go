package openmeter

import (
	"context"
)

// Price is one OpenMeter LLM price row, as the manual overrides list them.
type Price struct {
	Provider struct {
		ID string `json:"id"`
	} `json:"provider"`
	Model struct {
		ID string `json:"id"`
	} `json:"model"`
	Pricing map[string]string `json:"pricing"`
}

// Feature is a priced OpenMeter feature; the pack makes one per model
// resource, over the llm_tokens meter filtered to that resource.
type Feature struct {
	Meter struct {
		Filters map[string]map[string]string `json:"filters"`
	} `json:"meter"`
}

func (c *Client) Features(ctx context.Context) ([]Feature, error) {
	return listAll[Feature](ctx, c, "/api/v3/openmeter/features")
}

func (c *Client) Overrides(ctx context.Context) ([]Price, error) {
	return listAll[Price](ctx, c, "/api/v3/openmeter/llm-cost/overrides")
}
