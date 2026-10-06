package gateway

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
)

// Config migration 045 turns every rule into a policy of its own (§5.2). The
// golden file was recorded from the engine as it was before policies, over
// the seeded rules plus a few more (monitor, disabled, draft, a second
// reroute); this test replays the same requests through the policy engine
// and wants the same decisions.

var updateGolden = flag.Bool("update", false, "rewrite testdata/legacy_rules.golden.json")

// legacyRule is a row of policy_rules before migration 045: each rule had
// its own mode, fail mode and version.
type legacyRule struct {
	ID                                string
	Ordinal                           int
	Name, Description, Mode, FailMode string
	Version                           int
	When                              []model.Cond
	Then                              []model.Action
}

// legacyRules are the seeded rules as they were, then a few more.
var legacyRules = []legacyRule{
	{"r1", 1, "no-pii-out", "Redact customer identifiers before they leave the perimeter.", "enforce", "closed", 7,
		[]model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"email", "SSN"}}, {Field: "team", Op: "is not", Value: []string{"security"}}},
		[]model.Action{{Action: "redact", Detail: "email, SSN · rehydrate on return"}}},
	{"r2", 2, "eu-only", "EU customer traffic must stay in EU regions.", "enforce", "closed", 3,
		[]model.Cond{{Field: "header x-data-region", Op: "equals", Value: []string{"eu"}}},
		[]model.Action{{Action: "route to", Detail: "eu-private"}}},
	{"r3", 3, "block-src", "Block proprietary source code and secrets from third-party providers.", "enforce", "closed", 12,
		[]model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"secret", "private key", "source code"}}, {Field: "provider", Op: "is not", Value: []string{"Self-hosted"}}},
		[]model.Action{{Action: "block", Detail: "return 403 with rule id"}}},
	{"r4", 4, "card-numbers", "Luhn-validated card numbers are redacted everywhere.", "monitor", "closed", 1,
		[]model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"credit card"}}},
		[]model.Action{{Action: "redact", Detail: "credit card · no rehydrate"}}},
	{"r5", 5, "cost-guard-opus", "Downgrade long-context batch jobs off Opus.", "enforce", "open", 2,
		[]model.Cond{{Field: "model", Op: "equals", Value: []string{"claude-opus-4-1"}}, {Field: "team", Op: "is", Value: []string{"batch"}}},
		[]model.Action{{Action: "route to", Detail: "gpt-5-mini"}}},
	{"r6", 6, "support-eu", "Support goes to the EU.", "enforce", "open", 4,
		[]model.Cond{{Field: "team", Op: "is", Value: []string{"support"}}}, []model.Action{{Action: "route to", Detail: "eu-central"}}},
	{"r7", 7, "web-no-email", "Watch web for emails.", "monitor", "closed", 2,
		[]model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"email"}}, {Field: "team", Op: "is", Value: []string{"web"}}}, []model.Action{{Action: "block"}}},
	{"r8", 8, "off", "Disabled.", "disabled", "closed", 3,
		[]model.Cond{{Field: "team", Op: "is", Value: []string{"support"}}}, []model.Action{{Action: "block"}}},
	{"r9", 9, "wip", "Never published.", "draft", "closed", 0,
		[]model.Cond{{Field: "team", Op: "is", Value: []string{"support"}}}, []model.Action{{Action: "block"}}},
	{"r10", 10, "phones", "Phone numbers, kept.", "enforce", "open", 1,
		[]model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"phone"}}}, []model.Action{{Action: "redact", Detail: "phone · rehydrate on return"}}},
}

// fromLegacy is what migration 045 does to each rule, in Go: a policy of
// its own with the rule's id, name, description, mode, fail mode, version
// and place in the order, holding the rule under the same id and name. A
// never-published rule's content becomes the policy's draft, so it has no
// live rules.
func fromLegacy(rs []legacyRule) []model.Policy {
	out := make([]model.Policy, len(rs))
	for i, r := range rs {
		out[i] = model.Policy{ID: r.ID, Ordinal: r.Ordinal, Name: r.Name, Description: r.Description, Mode: r.Mode, FailMode: r.FailMode, Version: r.Version}
		if r.Version > 0 {
			out[i].Rules = []model.PolicyRule{{ID: r.ID, Name: r.Name, When: r.When, Then: r.Then}}
		}
	}
	return out
}

// The seed is what migrating the seeded rules gives, so a seeded database
// and a migrated one agree.
func TestSeededPoliciesAreTheMigratedRules(t *testing.T) {
	if got := fromLegacy(legacyRules[:5]); !reflect.DeepEqual(got, demo.Policies) {
		t.Fatalf("seed %+v\nmigrated %+v", demo.Policies, got)
	}
}

type legacyCase struct {
	Key, Model, Region, Prompt string
	Deadline                   bool // already past when rules start
	AllOpen                    bool // every rule fails open
}

func legacyCases() []legacyCase {
	prompts := []string{
		"hello",
		"mail jordan@example.com and 123-45-6789",
		"call (555) 123-4567 or jordan@example.com",
		"key sk-abcdefghijklmnopqrstuvwxyz",
		"card 4111 1111 1111 1111",
		"```go\nfunc main() {}\n```",
	}
	var out []legacyCase
	for _, k := range demo.Keys {
		if k.Status == "revoked" {
			continue
		}
		for _, m := range append(slices.Clone(k.AllowedModels), "claude-opus-4-1") {
			for _, region := range []string{"", "eu"} {
				for _, p := range prompts {
					out = append(out, legacyCase{Key: k.ID, Model: m, Region: region, Prompt: p})
				}
			}
		}
		out = append(out, legacyCase{Key: k.ID, Model: k.AllowedModels[0], Prompt: "mail a@b.com", Deadline: true},
			legacyCase{Key: k.ID, Model: k.AllowedModels[0], Prompt: "mail a@b.com", Deadline: true, AllOpen: true})
	}
	return out
}

// legacyOutcome is what a decision did, in the fields the engine had before
// policies (RuleEval's policy fields are new, so they're left out).
type legacyOutcome struct {
	Case        string
	Reject      string   `json:",omitempty"`
	Model       string   `json:",omitempty"`
	Backend     string   `json:",omitempty"`
	Verdict     string   `json:",omitempty"`
	RouteReason string   `json:",omitempty"`
	Prompt      string   `json:",omitempty"`
	Redactions  []string `json:",omitempty"`
	Rules       []string `json:",omitempty"`
	RulesStep   string   `json:",omitempty"`
	Vault       int      `json:",omitempty"`
	Mode        string   `json:",omitempty"`
}

func legacyDecide(s *Snapshot, c legacyCase) legacyOutcome {
	in := Input{Secret: secret(c.Key), Region: c.Region, Req: chat(c.Model, c.Prompt), Now: demoNow}
	if c.Deadline {
		in.Deadline = time.Now().Add(-time.Second)
	}
	d := Admit(s, in, rand.New(rand.NewPCG(1, 2)))
	o := legacyOutcome{Case: fmt.Sprintf("%s %s region=%q deadline=%v allOpen=%v %q", c.Key, c.Model, c.Region, c.Deadline, c.AllOpen, c.Prompt)}
	p := d.Policy(s, demoNow)
	o.Mode = p.Mode
	for _, e := range d.Receipt.Rules {
		o.Rules = append(o.Rules, fmt.Sprintf("%s %s v%d matched=%v %s", e.RuleID, e.Name, e.Version, e.Matched, e.Action))
	}
	for _, r := range d.Receipt.Redactions {
		o.Redactions = append(o.Redactions, fmt.Sprintf("%s×%d", r.Type, r.Count))
	}
	slices.Sort(o.Redactions)
	if d.Reject != nil {
		o.Reject = fmt.Sprintf("%d %s %s", d.Reject.Status, d.Reject.Code, d.Reject.Message)
		o.Verdict = p.Verdict
		o.RulesStep = p.Trace[1].Outcome
		return o
	}
	o.Model, o.Backend, o.Verdict, o.RouteReason = d.Req.Model, d.Candidates[0].Backend.Name, p.Verdict, d.Receipt.RouteReason
	o.Prompt = d.Req.Messages[0].Content
	o.RulesStep = sortRedacted(d.rulesStep.Outcome)
	o.Vault = d.Vault.Len()
	return o
}

// sortRedacted orders each run of "redacted …" outcomes: the engine before
// policies listed one rule's entities in map order.
func sortRedacted(step string) string {
	parts := strings.Split(step, " · ")
	for i := 0; i < len(parts); {
		j := i
		for j < len(parts) && strings.HasPrefix(parts[j], "redacted ") {
			j++
		}
		slices.Sort(parts[i:j])
		i = max(j, i+1)
	}
	return strings.Join(parts, " · ")
}

func TestMigratedPoliciesDecideAsTheRulesDid(t *testing.T) {
	var got []legacyOutcome
	for _, c := range legacyCases() {
		s := DemoSnapshot()
		rules := slices.Clone(legacyRules)
		if c.AllOpen {
			for i := range rules {
				rules[i].FailMode = "open"
			}
		}
		s.Policies = fromLegacy(rules)
		got = append(got, legacyDecide(s, c))
	}
	const path = "testdata/legacy_rules.golden.json"
	if *updateGolden {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []legacyOutcome
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d cases, golden has %d", len(got), len(want))
	}
	diffs := 0
	for i := range got {
		g, _ := json.Marshal(got[i])
		w, _ := json.Marshal(want[i])
		if string(g) != string(w) {
			diffs++
			if diffs <= 5 {
				t.Errorf("case %d:\n got %s\nwant %s", i, g, w)
			}
		}
	}
	if diffs > 0 {
		t.Fatalf("%d of %d decisions changed", diffs, len(got))
	}
	// The golden file covers what matters: every kind of outcome.
	seen := map[string]bool{}
	for _, o := range want {
		seen[o.Verdict] = true
		seen["mode "+o.Mode] = true
		if strings.Contains(o.Reject, "policy_deadline") {
			seen["deadline"] = true
		}
		if o.Vault > 0 {
			seen["vault"] = true
		}
		for _, r := range o.Rules {
			if strings.Contains(r, "would") {
				seen["monitor"] = true
			}
		}
	}
	for _, k := range []string{"allowed", "redacted", "rerouted", "blocked", "mode fail-open", "mode fail-closed", "deadline", "vault", "monitor"} {
		if !seen[k] {
			t.Errorf("golden file has no %s case", k)
		}
	}
}
