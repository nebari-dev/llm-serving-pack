package openmeter_test

import (
	"context"
	"fmt"
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

// OpenMeter stores prices as decimals and lists them in canonical form, so a
// price written as 3e-6 or 0.0000030 is the price it lists as 0.000003, and
// a rate left out is the one it lists as "0". Neither makes a new override.
func TestBootstrapComparesPricesAsNumbers(t *testing.T) {
	f := &fakeOpenMeter{}
	c := openmeter.NewClient(f.server(t).URL, "")
	cfg := openmeter.BootstrapConfig{Prices: []openmeter.ManualPrice{{Provider: "mock", Model: "mock/small",
		Pricing: map[string]string{"input_per_token": "3e-6", "output_per_token": "0.0000150"}}}}

	for range 2 {
		if err := openmeter.Bootstrap(context.Background(), c, cfg, time.Now); err != nil {
			t.Fatal(err)
		}
	}

	if len(f.created) != 1 {
		t.Fatalf("created %d overrides, want 1: %+v", len(f.created), f.created)
	}
}

// OpenMeter pages its lists. With more features and prices than fit one
// page, the ones on later pages are still found and not created again.
func TestBootstrapReadsEveryPage(t *testing.T) {
	f := &fakeOpenMeter{}
	cfg := openmeter.BootstrapConfig{}
	for i := range 150 {
		name := fmt.Sprintf("resource-%03d", i)
		f.features = append(f.features, feature("f-"+name, name, "bedrock"))
		f.overrides = append(f.overrides, map[string]any{"provider": map[string]any{"id": "mock"}, "model": map[string]any{"id": name},
			"pricing": map[string]any{"input_per_token": "0.001", "output_per_token": "0.002", "cache_read_per_token": "0", "cache_write_per_token": "0", "reasoning_per_token": "0"}})
	}
	cfg.Resources = []openmeter.Resource{{Name: "resource-149", Provider: "bedrock"}}
	cfg.Prices = []openmeter.ManualPrice{{Provider: "mock", Model: "resource-149", Pricing: map[string]string{"input_per_token": "0.001", "output_per_token": "0.002"}}}
	c := openmeter.NewClient(f.server(t).URL, "")

	if err := openmeter.Bootstrap(context.Background(), c, cfg, time.Now); err != nil {
		t.Fatal(err)
	}

	if len(f.created) != 0 {
		t.Fatalf("created %+v, want nothing: all of it is on page 2", f.created)
	}
}
