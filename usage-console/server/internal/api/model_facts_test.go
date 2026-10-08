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

// A model added with a provider starts with context 0 (unknown); its
// LiteLLM entries say how much it takes. The largest wins, since a backend
// that limits it lower still accepts what fits. A known context stays.
func TestUnknownContextComesFromLiteLLM(t *testing.T) {
	ms := []model.Model{{ID: "claude-opus-5-5"}, {ID: "gpt-5-mini", Context: 400_000}, {ID: "smollm2", Context: 8192}}
	facts := []store.PairFacts{
		{Model: "claude-opus-5-5", Backend: "testing-anthropic", Context: 1_000_000},
		{Model: "claude-opus-5-5", Backend: "bedrock-us", Context: 200_000},
		{Model: "gpt-5-mini", Backend: "openai-prod", Context: 272_000},
	}
	got := withFacts(ms, facts)
	if got[0].Context != 1_000_000 || got[1].Context != 400_000 || got[2].Context != 8192 {
		t.Fatalf("contexts = %d, %d, %d", got[0].Context, got[1].Context, got[2].Context)
	}
}
