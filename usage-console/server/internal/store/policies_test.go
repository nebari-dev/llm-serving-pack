package store

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
)

var ruleEnv = RuleEnv{
	Entities: []string{"email", "SSN", "secret"},
	Models:   []string{"gpt-5-mini"},
	Regions:  []string{"eu-private"},
}

func TestValidateRule(t *testing.T) {
	ok := model.PolicyRule{Name: "no-secrets",
		When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"secret"}}, {Field: "team", Op: "is not", Value: []string{"security"}}},
		Then: []model.Action{{Action: "block", Detail: "return 403"}}}
	redact, route := model.Action{Action: "redact"}, model.Action{Action: "route to", Detail: "eu-private"}
	for _, c := range []struct {
		name string
		edit func(*model.PolicyRule)
		err  string
	}{
		{"valid", func(*model.PolicyRule) {}, ""},
		{"route to model", func(r *model.PolicyRule) { r.Then = []model.Action{{Action: "route to", Detail: "gpt-5-mini"}} }, ""},
		{"route to region", func(r *model.PolicyRule) { r.Then = []model.Action{route} }, ""},
		{"redact", func(r *model.PolicyRule) { r.Then = []model.Action{redact} }, ""},
		// §5.3: a rule may redact and reroute.
		{"redact and route", func(r *model.PolicyRule) { r.Then = []model.Action{redact, route} }, ""},
		{"equals alias", func(r *model.PolicyRule) {
			r.When = []model.Cond{{Field: "header x-data-region", Op: "equals", Value: []string{"eu"}}}
		}, ""},
		{"name", func(r *model.PolicyRule) { r.Name = "No Secrets" }, "rule name must be lowercase letters, digits and dashes"},
		{"no conditions", func(r *model.PolicyRule) { r.When = nil }, "a rule needs at least one condition"},
		{"op", func(r *model.PolicyRule) { r.When[1].Op = "matches" }, `condition 2: unknown op "matches"`},
		{"field", func(r *model.PolicyRule) { r.When[1].Field = "user" }, `condition 2: unknown field "user"`},
		{"entity field", func(r *model.PolicyRule) { r.When[0].Field = "team" }, "condition 1: contains entity applies to the prompt"},
		{"entity", func(r *model.PolicyRule) { r.When[0].Value = []string{"passport"} }, `condition 1: unknown entity "passport"`},
		{"empty value", func(r *model.PolicyRule) { r.When[1].Value = nil }, "condition 2: needs at least one value"},
		{"no action", func(r *model.PolicyRule) { r.Then = nil }, "a rule needs at least one action"},
		{"two blocks", func(r *model.PolicyRule) { r.Then = append(r.Then, model.Action{Action: "block"}) }, "two block actions: one is enough"},
		{"block with others", func(r *model.PolicyRule) { r.Then = []model.Action{redact, {Action: "block"}, route} },
			"block wins: a blocked request is refused, so redact and route to would never run. Keep block alone, or move them to another rule"},
		{"two redacts", func(r *model.PolicyRule) { r.Then = []model.Action{redact, redact} }, "one redact per rule: it removes every entity the conditions find"},
		{"two routes", func(r *model.PolicyRule) { r.Then = []model.Action{route, route} }, "one route to per rule: only the last would apply"},
		{"action", func(r *model.PolicyRule) { r.Then = []model.Action{{Action: "allow"}} }, `unknown action "allow"`},
		{"route target", func(r *model.PolicyRule) { r.Then = []model.Action{{Action: "route to", Detail: "mars"}} }, `route to "mars": not a catalog model or backend region`},
		{"redact without entity", func(r *model.PolicyRule) {
			r.When = []model.Cond{{Field: "team", Op: "is", Value: []string{"web"}}}
			r.Then = []model.Action{redact}
		}, "redact needs a contains entity condition to know what to redact"},
	} {
		r := ok
		r.When = append([]model.Cond(nil), ok.When...)
		c.edit(&r)
		got := ""
		if err := ValidateRule(r, ruleEnv); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: got %q, want %q", c.name, got, c.err)
		}
	}
}

func blockRule(id, name string) model.PolicyRule {
	return model.PolicyRule{ID: id, Name: name, When: []model.Cond{{Field: "team", Op: "is", Value: []string{"web"}}}, Then: []model.Action{{Action: "block"}}}
}

func TestValidatePolicy(t *testing.T) {
	ok := PolicyContent{Name: "data-protection", FailMode: "closed", Rules: []model.PolicyRule{blockRule("", "no-web"), blockRule("", "no-web-2")}}
	for _, c := range []struct {
		name string
		edit func(*PolicyContent)
		err  string
	}{
		{"valid", func(*PolicyContent) {}, ""},
		{"name", func(p *PolicyContent) { p.Name = "Data" }, "name must be lowercase letters, digits and dashes"},
		{"fail mode", func(p *PolicyContent) { p.FailMode = "maybe" }, "failMode must be open or closed"},
		{"no rules", func(p *PolicyContent) { p.Rules = nil }, "a policy needs at least one rule"},
		{"same rule name", func(p *PolicyContent) { p.Rules[1].Name = "no-web" }, `two rules are named "no-web"`},
		{"same rule id", func(p *PolicyContent) { p.Rules[0].ID, p.Rules[1].ID = "r1", "r1" }, `rule "no-web-2": id "r1" is taken by another rule`},
		{"bad rule id", func(p *PolicyContent) { p.Rules[0].ID = "r 1" }, `rule "no-web": id "r 1" must be letters, digits, dashes or underscores`},
		{"rule error names the rule", func(p *PolicyContent) { p.Rules[1].Then = nil }, `rule "no-web-2": a rule needs at least one action`},
	} {
		p := ok
		p.Rules = append([]model.PolicyRule(nil), ok.Rules...)
		c.edit(&p)
		got := ""
		if err := ValidatePolicy(p, ruleEnv); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: got %q, want %q", c.name, got, c.err)
		}
	}
}

// New rules get an id; rules that had one keep it, so receipts and history
// follow a rule across versions.
func TestWithRuleIDs(t *testing.T) {
	n := 0
	gen := func() string { n++; return "rnew" + string(rune('0'+n)) }
	in := []model.PolicyRule{{ID: "r1", Name: "a"}, {Name: "b"}, {Name: "c"}}
	got := withRuleIDs(in, gen)
	if ids := []string{got[0].ID, got[1].ID, got[2].ID}; !reflect.DeepEqual(ids, []string{"r1", "rnew1", "rnew2"}) {
		t.Fatalf("ids %v", ids)
	}
	if in[1].ID != "" {
		t.Error("changed its input")
	}
}

// §5.3: reroute is last-write-wins, with a conflict warning when authoring.
func TestPolicyWarningsNameRerouteConflicts(t *testing.T) {
	route := func(id, name, to string) model.PolicyRule {
		return model.PolicyRule{ID: id, Name: name, When: []model.Cond{{Field: "team", Op: "is", Value: []string{"web"}}}, Then: []model.Action{{Action: "route to", Detail: to}}}
	}
	c := PolicyContent{Name: "routing", Rules: []model.PolicyRule{route("a", "to-eu", "eu-private"), blockRule("b", "no"), route("c", "to-mini", "gpt-5-mini")}}
	others := []model.Policy{
		{ID: "p0", Name: "early", Ordinal: 1, Mode: "enforce", Rules: []model.PolicyRule{route("x", "x", "eu-private")}},
		{ID: "p1", Name: "routing", Ordinal: 2, Mode: "enforce"}, // this policy as it's live now
		{ID: "p2", Name: "late", Ordinal: 3, Mode: "enforce", Rules: []model.PolicyRule{route("y", "y", "gpt-5-mini")}},
		{ID: "p3", Name: "watching", Ordinal: 4, Mode: "monitor", Rules: []model.PolicyRule{route("z", "z", "gpt-5-mini")}}, // changes nothing
		{ID: "p4", Name: "off", Ordinal: 5, Mode: "disabled", Rules: []model.PolicyRule{route("w", "w", "gpt-5-mini")}},
	}
	got := PolicyWarnings("p1", c, others)
	want := []string{
		"Rules to-eu and to-mini both reroute. When both match, to-mini's route to gpt-5-mini wins: the last reroute wins.",
		"Policy early runs before this one and reroutes too (rule x). When both match, this policy's route wins.",
		"Policy late runs after this one and reroutes too (rule y). When both match, its route wins.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := PolicyWarnings("p1", PolicyContent{Rules: []model.PolicyRule{blockRule("b", "no")}}, others); len(got) != 0 {
		t.Errorf("a policy that doesn't reroute: %q", got)
	}
	// A new policy goes last.
	if got := PolicyWarnings("", PolicyContent{Rules: []model.PolicyRule{route("a", "a", "gpt-5-mini")}}, others[:1]); len(got) != 1 || !strings.HasPrefix(got[0], "Policy early runs before") {
		t.Errorf("new policy: %q", got)
	}
}

func TestPlanPublish(t *testing.T) {
	content := PolicyContent{Name: "no-secrets", FailMode: "closed", Rules: []model.PolicyRule{blockRule("r1", "no-web")}}
	unpublished := model.Policy{ID: "p9", Name: "no-secrets", Mode: "draft", Version: 0}
	live := model.Policy{ID: "p9", Name: "no-secrets", Mode: "enforce", FailMode: "closed", Version: 4, Rules: content.Rules}
	for _, c := range []struct {
		name           string
		cur            model.Policy
		draft          *PolicyContent
		mode, failMode string
		wantMode       string
		wantVersion    int
		action, err    string
	}{
		{"first publish defaults to monitor", unpublished, &content, "", "", "monitor", 1, "Published policy in monitor mode", ""},
		{"first publish can enforce", unpublished, &content, "enforce", "", "enforce", 1, "Published policy", ""},
		{"nothing to publish", unpublished, nil, "", "", "", 0, "", "no draft to publish"},
		{"draft keeps the live mode", live, &content, "", "", "enforce", 5, "Published policy", ""},
		{"mode only", live, nil, "monitor", "", "monitor", 5, "Published policy in monitor mode", ""},
		{"fail mode only", live, nil, "", "open", "enforce", 5, "Published policy", ""},
		{"disable", live, nil, "disabled", "", "disabled", 5, "Disabled policy", ""},
		{"no change", live, nil, "enforce", "closed", "", 0, "", "nothing to publish: no draft, and mode and fail mode are unchanged"},
		{"bad mode", live, nil, "draft", "", "", 0, "", "mode must be enforce, monitor or disabled"},
		{"bad fail mode", live, nil, "", "sometimes", "", 0, "", "failMode must be open or closed"},
	} {
		next, action, err := planPublish(c.cur, c.draft, c.mode, c.failMode)
		if got := errString(err); got != c.err {
			t.Errorf("%s: err %q, want %q", c.name, got, c.err)
			continue
		}
		if err != nil {
			continue
		}
		if next.Mode != c.wantMode || next.Version != c.wantVersion || action != c.action {
			t.Errorf("%s: got %s v%d %q, want %s v%d %q", c.name, next.Mode, next.Version, action, c.wantMode, c.wantVersion, c.action)
		}
		if c.failMode != "" && next.FailMode != c.failMode {
			t.Errorf("%s: fail mode %s", c.name, next.FailMode)
		}
		if c.draft != nil && !reflect.DeepEqual(next.Rules, c.draft.Rules) {
			t.Errorf("%s: rules %+v", c.name, next.Rules)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A project condition names projects by id (§5.1), so a rule can't mean two
// teams' same-named projects at once, and a rename doesn't change it.
func TestValidateRuleNamesProjectsByID(t *testing.T) {
	env := ruleEnv
	env.Projects = []string{"p1a2b3c4d"}
	r := model.PolicyRule{Name: "helpdesk-only", Then: []model.Action{{Action: "block"}},
		When: []model.Cond{{Field: "project", Op: "is", Value: []string{"p1a2b3c4d"}}}}
	if err := ValidateRule(r, env); err != nil {
		t.Fatalf("by id: %v", err)
	}
	r.When[0].Value = []string{"p1a2b3c4d", "helpdesk"}
	if err := ValidateRule(r, env); err == nil || err.Error() != `condition 1: unknown project "helpdesk" (name projects by id)` {
		t.Fatalf("by name: %v", err)
	}
}
