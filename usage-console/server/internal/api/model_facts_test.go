package api

import (
	"reflect"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// A model's modalities are what any backend's LiteLLM entry says it takes;
// deprecations are per backend. With no entry, both stay unknown (nil).
func TestModelsCarryLiteLLMFacts(t *testing.T) {
	ms := []model.Model{{ID: "claude-haiku-4-5"}, {ID: "claude-sonnet-5"}, {ID: "smollm2"}}
	facts := []store.PairFacts{
		{Model: "claude-haiku-4-5", Backend: "bedrock-eu", Modalities: []string{"text", "image"}, Deprecation: "2026-10-15"},
		{Model: "claude-sonnet-5", Backend: "anthropic-prod", Modalities: []string{"text", "image", "pdf"}},
		{Model: "claude-sonnet-5", Backend: "bedrock-eu", Modalities: []string{"text", "image"}},
	}
	got := withFacts(ms, facts)
	want := []model.Model{
		{ID: "claude-haiku-4-5", Modalities: []string{"text", "image"}, Deprecations: []model.Deprecation{{Backend: "bedrock-eu", Date: "2026-10-15"}}},
		{ID: "claude-sonnet-5", Modalities: []string{"text", "image", "pdf"}},
		{ID: "smollm2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}
