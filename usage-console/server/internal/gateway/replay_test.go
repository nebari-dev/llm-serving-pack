package gateway

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
)

// captured is what Warden stores for a request on a capturing route: the
// prompt with every detected value masked.
func captured(s *Snapshot, prompt string) []fakellm.Message {
	msgs, _ := s.Detectors.Mask([]fakellm.Message{{Role: "user", Content: prompt}}, "")
	return msgs
}

func item(id, team, model string, msgs []fakellm.Message) ReplayItem {
	return ReplayItem{ID: id, TS: demoNow.UnixMilli(), KeyID: "k1", KeyName: "support-bot", Team: team, ProjectID: "p-helpdesk", Model: model, Messages: msgs}
}

func livePolicy(s *Snapshot, id string) model.Policy {
	i := slices.IndexFunc(s.Policies, func(p model.Policy) bool { return p.ID == id })
	return s.Policies[i]
}

func TestReplayOfTheLivePolicyChangesNothing(t *testing.T) {
	s := DemoSnapshot()
	r := NewReplay(s, livePolicy(s, "r1"))
	r.Add(item("a", "support", "gpt-5-mini", captured(s, "mail ana@example.com")))
	r.Add(item("b", "support", "gpt-5-mini", captured(s, "-----BEGIN RSA PRIVATE KEY-----")))
	r.Add(item("c", "batch", "claude-opus-4-1", nil))
	got := r.Result()
	if got.Total != 3 || got.Exact.Requests != 2 || got.Metadata.Requests != 1 {
		t.Fatalf("counts = %+v", got)
	}
	if got.Exact.Changed+got.Metadata.Changed != 0 || len(got.Affected) != 0 {
		t.Fatalf("an unchanged policy changed something: %+v", got)
	}
}

// Exact replay decides on the masked prompt as Warden decided on the real one.
func TestReplayOverCapturedContentIsExact(t *testing.T) {
	s := DemoSnapshot()
	prompt := "mail ana@example.com and bo@example.org"
	live := AdmitKey(s, s.KeyByID("k1"), Input{Req: chat("gpt-5-mini", prompt), Now: demoNow}, rand.New(rand.NewPCG(1, 2)))
	if len(live.Receipt.Redactions) != 1 || live.Receipt.Redactions[0].Count != 2 {
		t.Fatalf("live redactions = %+v", live.Receipt.Redactions)
	}

	// Drop no-pii-out's rules: the captured request is no longer redacted.
	off := livePolicy(s, "r1")
	off.Rules = nil
	r := NewReplay(s, off)
	r.Add(item("a", "support", "gpt-5-mini", captured(s, prompt)))
	r.Add(item("b", "support", "gpt-5-mini", captured(s, "nothing to see")))
	got := r.Result()
	if got.Exact.Changed != 1 || got.Exact.NoLongerRedacted != 1 {
		t.Fatalf("exact = %+v", got.Exact)
	}
	a := got.Affected[0]
	if a.ID != "a" || a.Kind != "exact" || a.From != "redacted 2 email" || a.To != "allowed" {
		t.Errorf("affected = %+v", a)
	}

	// Block email instead: the same request is newly blocked.
	block := livePolicy(s, "r1")
	block.Rules = []model.PolicyRule{{ID: "r1", Name: "no-pii-out", When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"email"}}},
		Then: []model.Action{{Action: "block"}}}}
	r = NewReplay(s, block)
	r.Add(item("a", "support", "gpt-5-mini", captured(s, prompt)))
	if got := r.Result(); got.Exact.NewlyBlocked != 1 || got.Affected[0].To != "blocked by no-pii-out" {
		t.Fatalf("block: %+v", got)
	}
}

// Without content, only rules that don't read the prompt run, on both sides,
// and the result names the rules skipped.
func TestReplayWithoutContentRunsMetadataRulesOnly(t *testing.T) {
	s := DemoSnapshot()
	guard := livePolicy(s, "r5")
	guard.Rules = slices.Clone(guard.Rules)
	guard.Rules[0].When = []model.Cond{{Field: "model", Op: "equals", Value: []string{"gpt-5.5"}}, {Field: "team", Op: "is", Value: []string{"support"}}}
	r := NewReplay(s, guard)
	r.Add(item("m", "support", "gpt-5.5", nil))
	r.Add(item("e", "support", "gpt-5-mini", nil)) // has no content: no-pii-out can't run on it
	got := r.Result()
	if got.Metadata.Requests != 2 || got.Metadata.Changed != 1 || got.Metadata.NewlyRerouted != 1 {
		t.Fatalf("metadata = %+v", got.Metadata)
	}
	if a := got.Affected[0]; a.ID != "m" || a.Kind != "metadata" || a.To != "rerouted to gpt-5-mini" {
		t.Errorf("affected = %+v", a)
	}
	for _, want := range []string{"no-pii-out", "block-src", "card-numbers"} {
		if !slices.Contains(got.SkippedRules, want) {
			t.Errorf("skipped rules %v lack %s", got.SkippedRules, want)
		}
	}
	if slices.Contains(got.SkippedRules, "eu-only") {
		t.Errorf("eu-only reads a header, not the prompt: %v", got.SkippedRules)
	}
}

// A monitoring policy's draft replays as if enforced: that's what promoting
// it would change.
func TestReplayEvaluatesTheDraftAsEnforced(t *testing.T) {
	s := DemoSnapshot()
	r := NewReplay(s, livePolicy(s, "r4")) // card-numbers, monitor
	r.Add(item("c", "support", "gpt-5-mini", captured(s, "card 4111 1111 1111 1111")))
	if got := r.Result(); got.Exact.NewlyRedacted != 1 {
		t.Fatalf("exact = %+v", got.Exact)
	}
}

// A request's region header is replayed from the receipt.
func TestReplayUsesTheRecordedRegion(t *testing.T) {
	s := DemoSnapshot()
	eu := livePolicy(s, "r2")
	eu.Rules = nil
	r := NewReplay(s, eu)
	it := item("r", "support", "gpt-5-mini", nil)
	it.Region = "eu"
	r.Add(it)
	if got := r.Result(); got.Metadata.NoLongerRerouted != 1 {
		t.Fatalf("metadata = %+v", got.Metadata)
	}
}
