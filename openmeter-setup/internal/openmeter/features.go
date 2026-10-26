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
	ID    string `json:"id"`
	Key   string `json:"key"`
	Meter struct {
		Filters map[string]map[string]string `json:"filters"`
	} `json:"meter"`
	UnitCost UnitCost `json:"unit_cost"`
}

// UnitCost is how a feature's usage is priced: OpenMeter's LLM unit cost
// fields. OpenMeter also returns the prices it resolved, which are ignored.
type UnitCost struct {
	Type              string `json:"type"`
	Provider          string `json:"provider,omitempty"`
	ProviderProperty  string `json:"provider_property,omitempty"`
	Model             string `json:"model,omitempty"`
	ModelProperty     string `json:"model_property,omitempty"`
	TokenType         string `json:"token_type,omitempty"`
	TokenTypeProperty string `json:"token_type_property,omitempty"`
	Amount            string `json:"amount,omitempty"` // a manual unit cost
}

func (c *Client) Features(ctx context.Context) ([]Feature, error) {
	return listAll[Feature](ctx, c, "/api/v3/openmeter/features")
}

func (c *Client) Overrides(ctx context.Context) ([]Price, error) {
	return listAll[Price](ctx, c, "/api/v3/openmeter/llm-cost/overrides")
}
