package openmeterapi_test

import (
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/shared/openmeterapi"
)

// OpenMeter looks prices up under these canonical IDs, so a price stored
// under anything else never matches. Cases from OpenMeter's own tests.
func TestNormalizeModelID(t *testing.T) {
	for _, c := range []struct{ provider, model, wantProvider, wantModel string }{
		{"OpenAI", "GPT-4o", "openai", "gpt-4o"},
		{" azure_ai ", "gpt-4", "azure", "gpt-4"},
		{"gemini", "gemini-2.5-pro", "google", "gemini-2-5-pro"},
		{"vertex_ai-language-models", "m", "vertex_ai", "m"},
		{"x-ai", "grok-4.6", "xai", "grok-4-6"},
		{"bedrock_converse", "us.anthropic.claude-haiku-4-5-20251001-v1:0", "bedrock", "anthropic.claude-haiku-4-5"},
		{"anthropic", "claude-3.5-sonnet-2024-10-22", "anthropic", "claude-3-5-sonnet"},
		{"deepseek", "deepseek-chat", "deepseek", "deepseek-v3"},
		{"vllm", "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4", "vllm", "qwen/qwen3-5-35b-a3b-gptq-int4"},
	} {
		p, m := openmeterapi.NormalizeModelID(c.provider, c.model)
		if p != c.wantProvider || m != c.wantModel {
			t.Errorf("NormalizeModelID(%q, %q) = %q, %q; want %q, %q", c.provider, c.model, p, m, c.wantProvider, c.wantModel)
		}
	}
}
