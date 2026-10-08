package store

import "testing"

// A model a backend adds gets a family, so savings (same family, cheaper)
// can compare it with its siblings: Opus and Haiku are both "claude".
func TestModelFamily(t *testing.T) {
	for _, c := range []struct{ provider, id, want string }{
		{"Anthropic", "claude-opus-5-5", "claude"},
		{"Anthropic", "claude-haiku-4-5-20251001", "claude"},
		{"OpenAI", "gpt-5-mini", "gpt"},
		{"OpenAI", "gpt-4o-mini", "gpt"},
		{"OpenRouter", "openai/gpt-4o-mini", "gpt"},
		{"OpenRouter", "anthropic/claude-sonnet-5", "claude"},
		{"Self-hosted", "llama-3.3-70b", "llama"},
		{"Self-hosted", "smollm2", "smollm"},
		{"Self-hosted", "ai/smollm2:360M-Q4_K_M", "smollm"},
		{"Self-hosted", "mistral-large-2411", "mistral-large"},
		{"Self-hosted", "flaky-model", "flaky-model"},
		{"Self-hosted", "Qwen2.5-7B", "qwen"},
	} {
		if got := ModelFamily(c.provider, c.id); got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.provider, c.id, got, c.want)
		}
	}
}
