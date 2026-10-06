package fakellm

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// Echo answers with the last user message as the provider received it, in
// pieces small enough that a placeholder spans several chunks.
func TestEchoRepeatsTheLastUserMessageInSmallChunks(t *testing.T) {
	p := Echo(ChatRequest{Model: "m", Messages: []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "mail [EMAIL_1] now"}}})
	if p.Status != 200 || p.Content() != "You said: mail [EMAIL_1] now" {
		t.Fatalf("plan = %+v", p)
	}
	split := false
	for _, c := range p.Chunks {
		if strings.Contains(c, "[") && !strings.Contains(c, "]") {
			split = true
		}
	}
	if !split || p.Usage.CompletionTokens == 0 || p.Usage.PromptTokens == 0 {
		t.Fatalf("chunks %q, usage %+v", p.Chunks, p.Usage)
	}
}

// OpenAI counts reasoning inside completion_tokens; total is prompt plus
// completion. Simulate reports reasoning apart, so this is the shape to check
// what Agent Router logs for a real reasoning model.
func TestOpenAIReasoningIsInsideCompletionTokens(t *testing.T) {
	p := Echo(ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	visible, prompt := p.Usage.CompletionTokens, p.Usage.PromptTokens
	p.Usage = OpenAIReasoning(p.Usage, 1000)
	u := p.Usage
	if u.CompletionTokens != visible+1000 || u.CompletionTokensDetails.ReasoningTokens != 1000 || u.TotalTokens != prompt+visible+1000 {
		t.Fatalf("usage %+v, visible %d", u, visible)
	}
}

// Simulated traffic reports reasoning as OpenAI does, inside
// completion_tokens, so the fake bills like the real thing.
func TestSimulateReportsReasoningInsideCompletion(t *testing.T) {
	seen := false
	for seed := range uint64(200) {
		p := Simulate("openai-prod", ChatRequest{Model: "gpt-5.5", Messages: []Message{{Role: "user", Content: "hi"}}}, rand.New(rand.NewPCG(seed, 1)))
		u := p.Usage
		if p.Status != 200 {
			continue
		}
		if u.TotalTokens != u.PromptTokens+u.CompletionTokens {
			t.Fatalf("total %d, want prompt %d + completion %d", u.TotalTokens, u.PromptTokens, u.CompletionTokens)
		}
		if r := u.CompletionTokensDetails.ReasoningTokens; r > 0 {
			seen = true
			if r > u.CompletionTokens {
				t.Fatalf("reasoning %d isn't inside completion %d", r, u.CompletionTokens)
			}
		}
	}
	if !seen {
		t.Fatal("no simulated request reasoned")
	}
}
