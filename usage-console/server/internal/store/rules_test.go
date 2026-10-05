package store

import (
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
)

var ruleEnv = RuleEnv{
	Entities: []string{"email", "SSN", "secret"},
	Models:   []string{"gpt-5-mini"},
	Regions:  []string{"eu-private"},
}

func TestValidateRule(t *testing.T) {
	ok := RuleContent{Name: "no-secrets", FailMode: "closed",
		When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"secret"}}, {Field: "team", Op: "is not", Value: []string{"security"}}},
		Then: []model.Action{{Action: "block", Detail: "return 403"}}}
	for _, c := range []struct {
		name string
		edit func(*RuleContent)
		err  string
	}{
		{"valid", func(*RuleContent) {}, ""},
		{"route to model", func(r *RuleContent) { r.Then = []model.Action{{Action: "route to", Detail: "gpt-5-mini"}} }, ""},
		{"route to region", func(r *RuleContent) { r.Then = []model.Action{{Action: "route to", Detail: "eu-private"}} }, ""},
		{"redact", func(r *RuleContent) { r.Then = []model.Action{{Action: "redact"}} }, ""},
		{"equals alias", func(r *RuleContent) {
			r.When = []model.Cond{{Field: "header x-data-region", Op: "equals", Value: []string{"eu"}}}
		}, ""},
		{"name", func(r *RuleContent) { r.Name = "No Secrets" }, "name must be lowercase letters, digits and dashes"},
		{"no name", func(r *RuleContent) { r.Name = "" }, "name must be lowercase letters, digits and dashes"},
		{"fail mode", func(r *RuleContent) { r.FailMode = "maybe" }, "failMode must be open or closed"},
		{"no conditions", func(r *RuleContent) { r.When = nil }, "a rule needs at least one condition"},
		{"op", func(r *RuleContent) { r.When[1].Op = "matches" }, `condition 2: unknown op "matches"`},
		{"field", func(r *RuleContent) { r.When[1].Field = "user" }, `condition 2: unknown field "user"`},
		{"entity field", func(r *RuleContent) { r.When[0].Field = "team" }, "condition 1: contains entity applies to the prompt"},
		{"entity", func(r *RuleContent) { r.When[0].Value = []string{"passport"} }, `condition 1: unknown entity "passport"`},
		{"empty value", func(r *RuleContent) { r.When[1].Value = nil }, "condition 2: needs at least one value"},
		{"no action", func(r *RuleContent) { r.Then = nil }, "a rule needs exactly one action"},
		{"two actions", func(r *RuleContent) { r.Then = append(r.Then, model.Action{Action: "redact"}) }, "a rule needs exactly one action"},
		{"action", func(r *RuleContent) { r.Then = []model.Action{{Action: "allow"}} }, `unknown action "allow"`},
		{"route target", func(r *RuleContent) { r.Then = []model.Action{{Action: "route to", Detail: "mars"}} }, `route to "mars": not a catalog model or backend region`},
		{"redact without entity", func(r *RuleContent) {
			r.When = []model.Cond{{Field: "team", Op: "is", Value: []string{"web"}}}
			r.Then = []model.Action{{Action: "redact"}}
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

func TestPlanPublish(t *testing.T) {
	content := RuleContent{Name: "no-secrets", FailMode: "closed",
		When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"secret"}}},
		Then: []model.Action{{Action: "block"}}}
	unpublished := model.PolicyRule{ID: "r9", Name: "no-secrets", Mode: "draft", Version: 0}
	live := model.PolicyRule{ID: "r9", Name: "no-secrets", Mode: "enforce", FailMode: "closed", Version: 4, When: content.When, Then: content.Then}
	for _, c := range []struct {
		name           string
		cur            model.PolicyRule
		draft          *RuleContent
		mode, failMode string
		wantMode       string
		wantVersion    int
		action, err    string
	}{
		{"first publish defaults to monitor", unpublished, &content, "", "", "monitor", 1, "Published rule in monitor mode", ""},
		{"first publish can enforce", unpublished, &content, "enforce", "", "enforce", 1, "Published rule", ""},
		{"nothing to publish", unpublished, nil, "", "", "", 0, "", "no draft to publish"},
		{"draft keeps the live mode", live, &content, "", "", "enforce", 5, "Published rule", ""},
		{"mode only", live, nil, "monitor", "", "monitor", 5, "Published rule in monitor mode", ""},
		{"fail mode only", live, nil, "", "open", "enforce", 5, "Published rule", ""},
		{"disable", live, nil, "disabled", "", "disabled", 5, "Disabled rule", ""},
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
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
