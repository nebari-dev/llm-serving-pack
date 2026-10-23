package openmeter_test

import (
	"context"
	"testing"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/openmeter-setup/internal/openmeter"
)

// Bootstrap gives each model resource a priced feature over the tokens
// meter, and sets the manual prices; run again (every upgrade), it changes
// nothing that is already so.
func TestBootstrapIsIdempotent(t *testing.T) {
	f := &fakeOpenMeter{features: []map[string]any{feature("f-mock", "mock-provider", "mock")}}
	c := openmeter.NewClient(f.server(t).URL, "")
	cfg := openmeter.BootstrapConfig{
		Resources: []openmeter.Resource{{Name: "mock-provider", Provider: "mock"}, {Name: "bedrock", Provider: "bedrock"}},
		Prices:    []openmeter.ManualPrice{{Provider: "mock", Model: "mock/small", Pricing: map[string]string{"input_per_token": "0.001", "output_per_token": "0.002"}}},
	}

	for range 2 {
		if err := openmeter.Bootstrap(context.Background(), c, cfg, time.Now); err != nil {
			t.Fatal(err)
		}
	}

	if len(f.created) != 2 {
		t.Fatalf("created %+v", f.created)
	}
	feat, ov := f.created[0], f.created[1]
	if feat["_kind"] != "feature" || feat["key"] != "bedrock_tokens" || feat["meter"].(map[string]any)["id"] != "m-tok" ||
		feat["meter"].(map[string]any)["filters"].(map[string]any)["model_resource"].(map[string]any)["eq"] != "bedrock" ||
		feat["unit_cost"].(map[string]any)["provider"] != "bedrock" || feat["unit_cost"].(map[string]any)["token_type_property"] != "token_type" {
		t.Fatalf("feature %+v", feat)
	}
	if ov["_kind"] != "override" || ov["provider"] != "mock" || ov["model_id"] != "mock/small" || ov["pricing"].(map[string]any)["input_per_token"] != "0.001" {
		t.Fatalf("override %+v", ov)
	}
}
