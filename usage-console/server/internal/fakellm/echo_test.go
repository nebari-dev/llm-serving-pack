package fakellm

import (
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
