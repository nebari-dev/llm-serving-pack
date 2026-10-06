package warden

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
	"github.com/jbouder/stargate/server/internal/traffic"
)

func newServer(snap *gateway.Snapshot) *Server {
	var cur gateway.Current
	cur.Store(snap)
	return &Server{Snap: &cur}
}

// headers are what reaches Warden after the key check admitted keyID.
func headers(snap *gateway.Snapshot, keyID string, extra ...string) map[string]string {
	k := snap.KeyByID(keyID)
	h := map[string]string{"x-stargate-key-id": k.ID, "x-stargate-team": k.Team, "x-stargate-project": k.Project, "x-request-id": "req-1"}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func body(m, user string) []byte {
	b, _ := json.Marshal(map[string]any{"model": m, "temperature": 0.2, "messages": []any{
		map[string]any{"role": "system", "content": "be brief", "name": "sys"},
		map[string]any{"role": "user", "content": user},
	}})
	return b
}

func policyOf(t *testing.T, r *extprocv3.ProcessingResponse) gateway.Policy {
	t.Helper()
	v := r.GetDynamicMetadata().GetFields()[MetadataNamespace].GetStructValue().GetFields()[MetadataKey].GetStringValue()
	var p gateway.Policy
	if err := json.Unmarshal([]byte(v), &p); err != nil {
		t.Fatalf("policy metadata %q: %v", v, err)
	}
	return p
}

func mutated(r *extprocv3.ProcessingResponse) (map[string]any, map[string]string) {
	c := r.GetRequestBody().GetResponse()
	hs := map[string]string{}
	for _, h := range c.GetHeaderMutation().GetSetHeaders() {
		hs[h.GetHeader().GetKey()] = string(h.GetHeader().GetRawValue())
	}
	b := c.GetBodyMutation().GetBody()
	if b == nil {
		return nil, hs
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m, hs
}

func TestRedactRewritesOnlyMessageContent(t *testing.T) {
	snap := gateway.DemoSnapshot()
	r := newServer(snap).decide(headers(snap, "k1"), body("gpt-5-mini", "mail jordan@example.com please"))
	p := policyOf(t, r)
	if p.Verdict != "redacted" || len(p.Redactions) != 1 || p.Redactions[0].Type != "email" {
		t.Fatalf("policy = %+v", p)
	}
	b, hs := mutated(r)
	if b == nil {
		t.Fatal("body not rewritten")
	}
	msgs := b["messages"].([]any)
	if got := msgs[1].(map[string]any)["content"]; got != "mail [EMAIL_1] please" {
		t.Errorf("user content = %q", got)
	}
	if msgs[0].(map[string]any)["name"] != "sys" || b["temperature"] != 0.2 || b["model"] != "gpt-5-mini" {
		t.Errorf("fields Warden doesn't own changed: %v", b)
	}
	if _, ok := hs[HeaderBackend]; ok {
		t.Error("backend hint set without a reroute")
	}
	if hs["content-length"] == "" {
		t.Error("content-length not updated")
	}
}

func TestBlockRuleAnswers403(t *testing.T) {
	snap := gateway.DemoSnapshot()
	r := newServer(snap).decide(headers(snap, "k1"), body("gpt-5-mini", "OPENAI_KEY=sk-"+strings.Repeat("a", 24)))
	ir := r.GetImmediateResponse()
	if ir.GetStatus().GetCode() != 403 || !strings.Contains(string(ir.GetBody()), "block-src") {
		t.Fatalf("immediate = %v", ir)
	}
	var keyHeader bool
	for _, h := range ir.GetHeaders().GetSetHeaders() {
		keyHeader = keyHeader || (h.GetHeader().GetKey() == gateway.HeaderKeyID && string(h.GetHeader().GetRawValue()) == "k1")
	}
	if !keyHeader {
		t.Error("403 lacks the key id the access log matches on")
	}
	p := policyOf(t, r)
	if p.Verdict != "blocked" || p.Blocked == nil || p.Blocked.ErrorCode != "policy_blocked" || p.Trace[1].State != "fail" {
		t.Fatalf("policy = %+v", p)
	}
}

func TestRerouteRewritesModelAndHintsBackend(t *testing.T) {
	snap := gateway.DemoSnapshot()
	r := newServer(snap).decide(headers(snap, "k3", "x-data-region", "eu"), body("summarize-digest", "hello"))
	p := policyOf(t, r)
	if p.Verdict != "rerouted" || p.RouteReason != "policy" || p.RequestedModel != "summarize-digest" {
		t.Fatalf("policy = %+v", p)
	}
	b, hs := mutated(r)
	if b["model"] != "llama-3.3-70b" || hs[HeaderBackend] != "vllm-internal" {
		t.Errorf("model %v, hint %q", b["model"], hs[HeaderBackend])
	}
}

func TestBudgetBlockAnswers429(t *testing.T) {
	snap := gateway.DemoSnapshot()
	snap.Spend.ByTeam["web"] = 16_000 // over b4's $15,000 block cap
	r := newServer(snap).decide(headers(snap, "k4"), body("gpt-5-mini", "hello"))
	if c := r.GetImmediateResponse().GetStatus().GetCode(); c != 429 {
		t.Fatalf("status = %d", c)
	}
	if p := policyOf(t, r); p.Blocked.ErrorCode != "budget_exceeded" {
		t.Errorf("policy = %+v", p)
	}
}

func TestProjectBudgetAnswers429(t *testing.T) {
	snap := gateway.DemoSnapshot()
	snap.Budgets["bp"] = model.Budget{ID: "bp", Scope: demo.ProjectID(demo.Tenant, "support", "helpdesk"), ScopeType: "project", Period: "monthly", CapUSD: 500, OnExceed: "block"}
	snap.Spend.ByKey["k1"] = 600
	r := newServer(snap).decide(headers(snap, "k1"), body("gpt-5-mini", "hello"))
	if c := r.GetImmediateResponse().GetStatus().GetCode(); c != 429 {
		t.Fatalf("status = %d", c)
	}
	p := policyOf(t, r)
	if p.Blocked == nil || p.Blocked.ErrorCode != "budget_exceeded" || p.Trace[0].Input != "project budget helpdesk · $600 of $500" {
		t.Errorf("policy = %+v", p)
	}
}

// Over a throttle cap each key gets gateway.ThrottleRate requests a minute.
// Warden is one process, so its counters are in memory and shared by every
// request it decides; the one over the rate is refused with Retry-After until
// the key's next slot, and recorded as throttled, not blocked.
func TestThrottleBudgetLimitsEachKeyWithRetryAfter(t *testing.T) {
	snap := gateway.DemoSnapshot()
	snap.Spend.ByTeam["support"] = 13_000 // over b1's throttle cap
	w := newServer(snap)
	now := time.Now() // rules check their deadline against the real clock; this only moves forward
	w.Now = func() time.Time { return now }
	for i := range gateway.ThrottleRate {
		r := w.decide(headers(snap, "k1"), body("gpt-5-mini", "hello"))
		p := policyOf(t, r)
		if r.GetImmediateResponse() != nil || p.Trace[0].State != "warn" || !strings.Contains(p.Trace[0].Outcome, "admitted") {
			t.Fatalf("request %d: policy = %+v", i+1, p)
		}
		now = now.Add(time.Second)
	}
	r := w.decide(headers(snap, "k1"), body("gpt-5-mini", "hello"))
	ir := r.GetImmediateResponse()
	if ir.GetStatus().GetCode() != 429 || !strings.Contains(string(ir.GetBody()), `"code":"budget_throttled"`) {
		t.Fatalf("immediate = %v", ir)
	}
	hs := map[string]string{}
	for _, h := range ir.GetHeaders().GetSetHeaders() {
		hs[h.GetHeader().GetKey()] = string(h.GetHeader().GetRawValue())
	}
	// The first of the ten leaves the window 60s after it came, 50s from now.
	if hs["retry-after"] != "50" || hs[gateway.HeaderKeyID] != "k1" {
		t.Errorf("headers %v", hs)
	}
	p := policyOf(t, r)
	if p.Verdict != "throttled" || p.Blocked == nil || p.Blocked.ErrorCode != "budget_throttled" || p.Blocked.Status != 429 || p.Trace[0].State != "throttle" {
		t.Fatalf("policy = %+v", p)
	}
	// The slot opens when Retry-After said.
	now = now.Add(50 * time.Second)
	if r := w.decide(headers(snap, "k1"), body("gpt-5-mini", "hello")); r.GetImmediateResponse() != nil {
		t.Fatalf("after Retry-After: %v", r.GetImmediateResponse())
	}
}

func TestBudgetBlockCarriesNoRetryAfter(t *testing.T) {
	snap := gateway.DemoSnapshot()
	snap.Spend.ByTeam["web"] = 16_000
	ir := newServer(snap).decide(headers(snap, "k4"), body("gpt-5-mini", "hello")).GetImmediateResponse()
	for _, h := range ir.GetHeaders().GetSetHeaders() {
		if h.GetHeader().GetKey() == "retry-after" {
			t.Fatal("a block isn't something to retry")
		}
	}
}

func TestMonitorRuleOnlyRecordsWould(t *testing.T) {
	snap := gateway.DemoSnapshot()
	r := newServer(snap).decide(headers(snap, "k1"), body("gpt-5-mini", "card 4111 1111 1111 1111"))
	p := policyOf(t, r)
	if b, _ := mutated(r); b != nil || p.Verdict != "allowed" {
		t.Fatalf("monitor rule acted: %+v", p)
	}
	var would bool
	for _, e := range p.Rules {
		would = would || (e.Name == "card-numbers" && e.Action == "would redact")
	}
	if !would {
		t.Errorf("rules = %+v", p.Rules)
	}
}

func slow(d time.Duration) func(*gateway.Snapshot, *store.KeyRecord, gateway.Input) *gateway.Decision {
	return func(s *gateway.Snapshot, k *store.KeyRecord, in gateway.Input) *gateway.Decision {
		time.Sleep(d)
		return gateway.AdmitKey(s, k, in, rand.New(rand.NewPCG(1, 2)))
	}
}

func TestDeadlineFailsClosed(t *testing.T) {
	snap := gateway.DemoSnapshot()
	w := newServer(snap)
	w.Deadline, w.evaluate = 10*time.Millisecond, slow(200*time.Millisecond)
	t0 := time.Now()
	r := w.decide(headers(snap, "k1"), body("gpt-5-mini", "hello"))
	if el := time.Since(t0); el > 100*time.Millisecond {
		t.Errorf("waited %v past a 10ms deadline", el)
	}
	if c := r.GetImmediateResponse().GetStatus().GetCode(); c != 503 {
		t.Fatalf("status = %d", c)
	}
	p := policyOf(t, r)
	if p.Mode != "fail-closed" || p.Blocked.ErrorCode != "policy_unavailable" {
		t.Errorf("policy = %+v", p)
	}
	if p.RequestedModel != "gpt-5-mini" || p.Blocked.ResolvedModel != "gpt-5-mini" {
		t.Errorf("refused receipt lost the model: requested %q, resolved %q", p.RequestedModel, p.Blocked.ResolvedModel)
	}
}

func TestDeadlineFailsOpenWhenEveryRuleDoes(t *testing.T) {
	snap := gateway.DemoSnapshot()
	for i := range snap.Policies {
		snap.Policies[i].FailMode = "open"
	}
	w := newServer(snap)
	w.Deadline, w.evaluate = 10*time.Millisecond, slow(200*time.Millisecond)
	r := w.decide(headers(snap, "k1"), body("gpt-5-mini", "mail jordan@example.com"))
	if r.GetImmediateResponse() != nil {
		t.Fatal("blocked")
	}
	if b, _ := mutated(r); b != nil {
		t.Error("rewrote the body without a decision")
	}
	if p := policyOf(t, r); p.Mode != "fail-open" || p.Verdict != "allowed" {
		t.Errorf("policy = %+v", p)
	}
}

func TestRuleReachedAfterDeadlineUsesItsFailMode(t *testing.T) {
	snap := gateway.DemoSnapshot()
	k := snap.KeyByID("k1")
	in := gateway.Input{Req: fakellm.ChatRequest{Model: "gpt-5-mini", Messages: []fakellm.Message{{Role: "user", Content: "hi"}}},
		Now: time.Now(), Deadline: time.Now().Add(-time.Millisecond)}
	d := gateway.AdmitKey(snap, k, in, rand.New(rand.NewPCG(1, 2)))
	if d.Reject == nil || d.Reject.Code != "policy_deadline" || !strings.Contains(d.Reject.Message, "no-pii-out") {
		t.Fatalf("reject = %+v", d.Reject)
	}
	if p := d.Policy(snap, time.Now()); p.Mode != "fail-closed" || p.RequestedModel != "gpt-5-mini" {
		t.Errorf("policy mode %q, model %q", p.Mode, p.RequestedModel)
	}
	for i := range snap.Policies {
		snap.Policies[i].FailMode = "open"
	}
	d = gateway.AdmitKey(snap, k, in, rand.New(rand.NewPCG(1, 2)))
	if d.Reject != nil || !strings.Contains(d.Receipt.Rules[0].Action, "fails open") {
		t.Fatalf("reject %+v, rules %+v", d.Reject, d.Receipt.Rules)
	}
	if p := d.Policy(snap, time.Now()); p.Mode != "fail-open" {
		t.Errorf("policy mode %q", p.Mode)
	}
}

func TestPanicFailsToFailMode(t *testing.T) {
	snap := gateway.DemoSnapshot()
	w := newServer(snap)
	w.evaluate = func(*gateway.Snapshot, *store.KeyRecord, gateway.Input) *gateway.Decision { panic("boom") }
	r := w.decide(headers(snap, "k1"), body("gpt-5-mini", "hello"))
	if p := policyOf(t, r); p.Mode != "fail-closed" || !strings.Contains(p.Trace[0].Outcome, "panicked") {
		t.Fatalf("policy = %+v", p)
	}
}

func TestKillSwitchPassesThrough(t *testing.T) {
	snap := gateway.DemoSnapshot()
	w := newServer(snap)
	w.SetPassthrough(true)
	r := w.decide(headers(snap, "k1"), body("gpt-5-mini", "OPENAI_KEY=sk-"+strings.Repeat("a", 24)))
	if r.GetImmediateResponse() != nil {
		t.Fatal("blocked in pass-through")
	}
	if p := policyOf(t, r); p.Mode != "passthrough" || p.Verdict != "allowed" {
		t.Errorf("policy = %+v", p)
	}
}

func TestWrongFilterOrderFailsMode(t *testing.T) {
	snap := gateway.DemoSnapshot()
	r := newServer(snap).decide(headers(snap, "k1", headerAfterAgentRouter, "/v1/chat/completions"), body("gpt-5-mini", "hello"))
	if p := policyOf(t, r); p.Mode != "fail-closed" {
		t.Fatalf("policy = %+v", p)
	}
}

func TestUnknownKeyIsEvaluatedAndRefreshes(t *testing.T) {
	snap := gateway.DemoSnapshot()
	w := newServer(snap)
	var asked bool
	w.Refresh = func() { asked = true }
	h := map[string]string{"x-stargate-key-id": "k_new", "x-stargate-team": "support", "x-stargate-project": "p"}
	p := policyOf(t, w.decide(h, body("gpt-5-mini", "mail jordan@example.com")))
	if !asked || p.Verdict != "redacted" {
		t.Fatalf("refresh %v, policy %+v", asked, p)
	}
}

// Warden and devgateway run the same engine, so the generated mix gets the
// same verdict either way.
func TestSameVerdictAsDevgateway(t *testing.T) {
	snap := gateway.DemoSnapshot()
	snap.Spend.ByTeam["web"] = 16_000
	w := newServer(snap)
	gen := traffic.New()
	r := rand.New(rand.NewPCG(7, 7))
	seen := map[string]int{}
	for range 2000 {
		req := gen.Next(r)
		// A fixed clock inside every seeded key's validity: the key check, not
		// Warden, refuses expired keys.
		now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		dev := gateway.Admit(snap, gateway.Input{Secret: req.Secret, Region: req.Region, Req: req.Body, Now: now}, r)
		want := "allowed"
		switch {
		case dev.Reject != nil:
			want = "blocked"
		case len(dev.Receipt.Redactions) > 0:
			want = "redacted"
		case dev.Rerouted():
			want = "rerouted"
		}
		if dev.Reject != nil && dev.Reject.Code == "model_not_allowed" {
			continue // the key check refuses these before Warden sees them
		}
		k := snap.KeyBy[demo.HashSecret(req.Secret)]
		b, _ := json.Marshal(req.Body)
		h := map[string]string{"x-stargate-key-id": k.ID, "x-stargate-team": k.Team, "x-stargate-project": k.Project}
		if req.Region != "" {
			h["x-data-region"] = req.Region
		}
		p := policyOf(t, w.decide(h, b))
		if p.Verdict != want {
			t.Fatalf("%s %s region=%q: warden %s, devgateway %s", k.Name, req.Body.Model, req.Region, p.Verdict, want)
		}
		seen[want]++
	}
	for _, v := range []string{"allowed", "blocked", "redacted", "rerouted"} {
		if seen[v] == 0 {
			t.Errorf("mix never produced %s: %v", v, seen)
		}
	}
}
