package api

import (
	"reflect"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func TestRuleVocabularyIsWhatValidateRuleAccepts(t *testing.T) {
	env := store.RuleEnv{
		Entities: []string{"SSN", "email"},
		Models:   []string{"gpt-5-mini", "claude-sonnet-5"},
		Regions:  []string{"us-east", "eu-private", "us-east"},
	}
	got := ruleVocabulary(env)
	want := RuleVocabulary{
		Entities: []string{"SSN", "email"},
		Fields:   store.RuleFields,
		Targets:  []string{"claude-sonnet-5", "eu-private", "gpt-5-mini", "us-east"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	ok := func(c model.PolicyRule) {
		t.Helper()
		if err := store.ValidateRule(c, env); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	base := model.PolicyRule{Name: "r", Then: []model.Action{{Action: "block"}}}
	for _, e := range got.Entities {
		c := base
		c.When = []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{e}}}
		ok(c)
	}
	for _, f := range got.Fields {
		c := base
		c.When = []model.Cond{{Field: f, Op: "is", Value: []string{"x"}}}
		ok(c)
	}
	for _, to := range got.Targets {
		c := base
		c.When = []model.Cond{{Field: "team", Op: "is", Value: []string{"x"}}}
		c.Then = []model.Action{{Action: "route to", Detail: to}}
		ok(c)
	}
}

// A publish's dry run lists what changes on the policy, its rules as one
// field: they're versioned together.
func TestPolicyChangesTreatRulesAsOne(t *testing.T) {
	r := model.PolicyRule{ID: "r1", Name: "a", When: []model.Cond{{Field: "team", Op: "is", Value: []string{"web"}}}, Then: []model.Action{{Action: "block"}}}
	cur := model.Policy{ID: "p1", Name: "p", Mode: "monitor", FailMode: "closed", Version: 3, Rules: []model.PolicyRule{r}}
	next := cur
	next.Mode, next.Version = "enforce", 4
	next.Rules = []model.PolicyRule{r, {ID: "r2", Name: "b", When: r.When, Then: []model.Action{{Action: "route to", Detail: "eu-private"}}}}
	var fields []string
	for _, c := range policyChanges(cur, next) {
		fields = append(fields, c.Field)
	}
	if want := []string{"mode", "rules", "version"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("changes %v, want %v", fields, want)
	}
}
