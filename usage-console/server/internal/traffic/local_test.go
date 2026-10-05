package traffic

import (
	"math/rand/v2"
	"testing"
)

// Requests to a real model stay small: a 360M model on a laptop has an 8K
// context, and OpenRouter bills each token.
func TestRealUpstreamRequestsStaySmall(t *testing.T) {
	g, r := New(), rand.New(rand.NewPCG(1, 2))
	seen := 0
	for range 20000 {
		q := g.Next(r)
		if m := q.Body.Model; m != "smollm2" && m != "gpt-4o-mini" {
			continue
		}
		seen++
		n := 0
		for _, msg := range q.Body.Messages {
			n += len(msg.Content) / 4
		}
		if n > 600 || q.Body.MaxTokens > 128 {
			t.Fatalf("%s request: ~%d prompt tokens, max_tokens %d", q.Body.Model, n, q.Body.MaxTokens)
		}
	}
	if seen == 0 {
		t.Fatal("no requests to the real upstreams")
	}
}
