package gateway

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/model"
)

// §5.3's ordering: policies in order, a policy's rules in list order; the
// first block wins and short-circuits; redacts accumulate; the last reroute
// wins. A rule may have several actions.

func rule(id string, when []model.Cond, then ...model.Action) model.PolicyRule {
	return model.PolicyRule{ID: id, Name: id, When: when, Then: then}
}

func policy(id, mode, failMode string, version int, rules ...model.PolicyRule) model.Policy {
	return model.Policy{ID: id, Name: id, Mode: mode, FailMode: failMode, Version: version, Rules: rules}
}

var (
	support   = []model.Cond{{Field: "team", Op: "is", Value: []string{"support"}}}
	hasEmail  = []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"email"}}}
	hasSSN    = []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"SSN"}}}
	hasSecret = []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"secret"}}}
	redactIt  = model.Action{Action: "redact", Detail: "· no rehydrate"}
	blockIt   = model.Action{Action: "block"}
)

func routeTo(to string) model.Action { return model.Action{Action: "route to", Detail: to} }

func admit(t *testing.T, ps []model.Policy, key, prompt string) *Decision {
	t.Helper()
	s := DemoSnapshot()
	s.Policies = ps
	return Admit(s, Input{Secret: secret(key), Req: chat("gpt-5-mini", prompt), Now: demoNow}, rand.New(rand.NewPCG(1, 2)))
}

func evals(d *Decision) []string {
	var out []string
	for _, e := range d.Receipt.Rules {
		out = append(out, e.PolicyID+"/"+e.RuleID+" "+e.Action)
	}
	return out
}

func TestFirstBlockWinsAndShortCircuits(t *testing.T) {
	ps := []model.Policy{
		policy("data", "enforce", "closed", 2,
			rule("pii", hasEmail, redactIt),
			rule("no-secrets", hasSecret, blockIt),
			rule("later", support, routeTo("llama-3.3-70b"))),
		policy("after", "enforce", "closed", 1, rule("never", support, blockIt)),
	}
	d := admit(t, ps, "k1", "mail a@b.com key sk-abcdefghijklmnopqrstuvwxyz")
	if d.Reject == nil || d.Reject.Code != "policy_blocked" {
		t.Fatalf("reject %+v", d.Reject)
	}
	if want := `Rule data/no-secrets v2 matched entity "secret". Remove it from the prompt, or route through a self-hosted backend.`; d.Reject.Message != want {
		t.Errorf("message %q", d.Reject.Message)
	}
	if got := strings.Join(evals(d), ", "); got != "data/pii redact, data/no-secrets block" {
		t.Errorf("evaluated %s", got)
	}
	if e := d.Receipt.Rules[1]; e.Policy != "data" || e.Name != "no-secrets" || e.Version != 2 {
		t.Errorf("eval %+v", e)
	}
	if d.blockedBy != `blocked by data/no-secrets v2 on entity "secret"` {
		t.Errorf("blocked by %q", d.blockedBy)
	}
}

func TestRedactsAccumulateAcrossRulesAndPolicies(t *testing.T) {
	ps := []model.Policy{
		policy("a", "enforce", "closed", 1, rule("emails", hasEmail, redactIt)),
		policy("b", "enforce", "closed", 1, rule("ssns", hasSSN, redactIt)),
	}
	d := admit(t, ps, "k1", "mail a@b.com about 123-45-6789")
	if d.Reject != nil {
		t.Fatal(d.Reject)
	}
	if got := d.Req.Messages[0].Content; got != "mail [EMAIL_1] about [SSN_1]" {
		t.Errorf("prompt %q", got)
	}
	if len(d.Receipt.Redactions) != 2 {
		t.Errorf("redactions %+v", d.Receipt.Redactions)
	}
}

func TestLastRerouteWins(t *testing.T) {
	ps := []model.Policy{
		policy("cost", "enforce", "open", 1,
			rule("first", support, routeTo("claude-haiku-4-5")),
			rule("second", support, routeTo("llama-3.3-70b"))),
	}
	d := admit(t, ps, "k1", "hi")
	if d.Req.Model != "llama-3.3-70b" || d.Candidates[0].Backend.Name != "vllm-internal" || !d.Rerouted() {
		t.Fatalf("model %s via %s", d.Req.Model, d.Candidates[0].Backend.Name)
	}
	// Across policies too: the later policy's reroute wins.
	ps = append(ps, policy("eu", "enforce", "closed", 3, rule("eu", support, routeTo("claude-haiku-4-5"))))
	if d = admit(t, ps, "k1", "hi"); d.Req.Model != "claude-haiku-4-5" {
		t.Fatalf("model %s", d.Req.Model)
	}
}

func TestRuleWithSeveralActionsAppliesEach(t *testing.T) {
	ps := []model.Policy{policy("pii-eu", "enforce", "closed", 4, rule("pii", hasEmail, redactIt, routeTo("llama-3.3-70b")))}
	d := admit(t, ps, "k1", "mail a@b.com")
	if d.Reject != nil {
		t.Fatal(d.Reject)
	}
	if d.Req.Messages[0].Content != "mail [EMAIL_1]" || d.Req.Model != "llama-3.3-70b" {
		t.Fatalf("prompt %q, model %s", d.Req.Messages[0].Content, d.Req.Model)
	}
	if got := evals(d); len(got) != 1 || got[0] != "pii-eu/pii redact + route to" {
		t.Errorf("evals %v", got)
	}
	if want := "redacted 1 email · pii-eu/pii matched → route to llama-3.3-70b"; d.rulesStep.Outcome != want {
		t.Errorf("rules step %q", d.rulesStep.Outcome)
	}
}

// A block in a rule with other actions wins: validation refuses that rule,
// but the engine doesn't depend on it.
func TestBlockBeatsTheRestOfItsRule(t *testing.T) {
	ps := []model.Policy{policy("p", "enforce", "closed", 1, rule("r", hasEmail, redactIt, blockIt))}
	d := admit(t, ps, "k1", "mail a@b.com")
	if d.Reject == nil || d.Reject.Code != "policy_blocked" || len(d.Receipt.Redactions) != 0 {
		t.Fatalf("reject %+v, redactions %+v", d.Reject, d.Receipt.Redactions)
	}
}

func TestMonitorPolicyRecordsEveryActionAsWould(t *testing.T) {
	ps := []model.Policy{policy("watch", "monitor", "closed", 1,
		rule("pii", hasEmail, redactIt, routeTo("llama-3.3-70b")),
		rule("secrets", hasSecret, blockIt))}
	d := admit(t, ps, "k1", "mail a@b.com key sk-abcdefghijklmnopqrstuvwxyz")
	if d.Reject != nil || d.Rerouted() || d.Req.Messages[0].Content != "mail a@b.com key sk-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("monitor acted: %+v %q", d.Reject, d.Req.Messages[0].Content)
	}
	if got := strings.Join(evals(d), ", "); got != "watch/pii would redact + route to, watch/secrets would block" {
		t.Errorf("evals %s", got)
	}
}

func TestDraftAndDisabledPoliciesAreSkipped(t *testing.T) {
	ps := []model.Policy{
		policy("wip", "draft", "closed", 0, rule("r", support, blockIt)),
		policy("off", "disabled", "closed", 2, rule("r", support, blockIt)),
	}
	d := admit(t, ps, "k1", "hi")
	if d.Reject != nil || len(d.Receipt.Rules) != 0 {
		t.Fatalf("reject %+v, evals %v", d.Reject, evals(d))
	}
}

// A rule reached after the deadline takes its policy's fail mode.
func TestPolicyFailModeDecidesPastTheDeadline(t *testing.T) {
	s := DemoSnapshot()
	s.Policies = []model.Policy{
		policy("cost", "enforce", "open", 2, rule("a", support, routeTo("llama-3.3-70b")), rule("b", support, routeTo("claude-haiku-4-5"))),
		policy("data", "enforce", "closed", 5, rule("pii", hasEmail, redactIt)),
	}
	in := Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "hi"), Now: demoNow, Deadline: time.Now().Add(-time.Second)}
	d := Admit(s, in, rand.New(rand.NewPCG(1, 2)))
	if d.Reject == nil || d.Reject.Code != "policy_deadline" || d.Reject.Message != "Rule data/pii v5 couldn't be evaluated in time and fails closed. Retry the request." {
		t.Fatalf("reject %+v", d.Reject)
	}
	if got := strings.Join(evals(d), ", "); got != "cost/a not evaluated · deadline · fails open, cost/b not evaluated · deadline · fails open, data/pii not evaluated · deadline · fails closed" {
		t.Errorf("evals %s", got)
	}
	if s.RuleCount() != 3 {
		t.Errorf("rule count %d", s.RuleCount())
	}
}
