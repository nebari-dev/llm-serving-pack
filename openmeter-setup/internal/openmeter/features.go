package openmeter

import (
	"context"
	"net/url"
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
	var out struct {
		Data []Feature `json:"data"`
	}
	return out.Data, c.get(ctx, "/api/v3/openmeter/features", url.Values{"page[size]": {"100"}}, &out)
}

func (c *Client) Overrides(ctx context.Context) ([]Price, error) {
	var out struct {
		Data []Price `json:"data"`
	}
	return out.Data, c.get(ctx, "/api/v3/openmeter/llm-cost/overrides", url.Values{"page[size]": {"100"}}, &out)
}
