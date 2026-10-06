package api

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

func TestDetectorViewsJoinLiveRulesAndReceiptCounts(t *testing.T) {
	ds := []gateway.DetectorInfo{
		{Entity: "SSN", Kind: "regex", Pattern: `\d`, Placeholder: "[SSN_1]"},
		{Entity: "email", Kind: "regex", Pattern: `@`, Placeholder: "[EMAIL_1]"},
		{Entity: "secret", Kind: "regex", Pattern: `sk-`, Placeholder: "[SECRET_1]"},
	}
	pii := []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"email", "SSN"}}}
	one := func(name string, version int, mode string, when []model.Cond, then ...model.Action) model.Policy {
		return model.Policy{Name: name, Version: version, Mode: mode, Rules: []model.PolicyRule{{Name: name, When: when, Then: then}}}
	}
	policies := []model.Policy{
		one("no-pii-out", 8, "monitor", pii, model.Action{Action: "redact"}),
		// A policy's rules each use what they name; a rule's actions are all listed.
		{Name: "data", Version: 2, Mode: "enforce", Rules: []model.PolicyRule{
			{Name: "pii-block", When: append(pii, model.Cond{Field: "team", Op: "is", Value: []string{"batch"}}), Then: []model.Action{{Action: "block"}}},
			{Name: "pii-eu", When: pii[:1], Then: []model.Action{{Action: "redact"}, {Action: "route to", Detail: "eu-private"}}},
		}},
		// Not live: Warden skips them, so they don't use a detector.
		one("off", 3, "disabled", pii, model.Action{Action: "block"}),
		one("wip", 0, "draft", pii, model.Action{Action: "block"}),
		// "is" on a field isn't a detector, even if a value looks like one.
		one("teams", 1, "enforce", []model.Cond{{Field: "team", Op: "is", Value: []string{"secret"}}}, model.Action{Action: "block"}),
	}
	hits := map[string]store.DetectorHits{
		"email": {RedactedRequests: 40, RedactedMatches: 52, Blocked: 3},
		"phone": {RedactedRequests: 9}, // no detector by that name any more
	}
	verdicts := map[string]store.VerdictCounts{"email": {FalsePositives: 2, Confirmed: 5}}
	got := detectorViews(ds, nil, policies, hits, verdicts)
	uses := []DetectorUse{{"no-pii-out", "no-pii-out", 8, "monitor", "redact"}, {"pii-block", "data", 2, "enforce", "block"}, {"pii-eu", "data", 2, "enforce", "redact + route to"}}
	want := []DetectorView{
		{DetectorInfo: ds[0], UsedBy: uses},
		{DetectorInfo: ds[1], UsedBy: uses, RedactedRequests24h: 40, RedactedMatches24h: 52, Blocked24h: 3, FalsePositives30d: 2, Confirmed30d: 5},
		{DetectorInfo: ds[2], UsedBy: []DetectorUse{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDetectorListMatchesWhatRulesCanName(t *testing.T) {
	var names []string
	for _, d := range gateway.DetectorList() {
		names = append(names, d.Entity)
		if d.Pattern == "" || d.Placeholder == "" || d.Kind == "" {
			t.Errorf("%s: incomplete %+v", d.Entity, d)
		}
	}
	if !reflect.DeepEqual(names, gateway.Entities()) {
		t.Fatalf("detectors %v, entities %v", names, gateway.Entities())
	}
}

// A custom entity's row carries the entity itself (examples, etag, who last
// changed it), so the console can edit it with If-Match.
func TestDetectorViewsCarryTheCustomEntity(t *testing.T) {
	ce := store.CustomEntity{ID: "ce1", Name: "employee ID", Pattern: `EMP-\d{6}`, Label: "EMPLOYEE", ETag: `"e1"`}
	ds := []gateway.DetectorInfo{{Entity: "email", Kind: "regex", Pattern: "@", Placeholder: "[EMAIL_1]"}, {Entity: "employee ID", Kind: "custom regex", Pattern: ce.Pattern, Placeholder: "[EMPLOYEE_1]", Custom: true}}
	got := detectorViews(ds, []store.CustomEntity{ce}, nil, nil, nil)
	if got[0].CustomEntity != nil || got[1].CustomEntity == nil || got[1].CustomEntity.ETag != `"e1"` {
		t.Fatalf("got %+v", got)
	}
}

// A receipt's detector hits are what it records per entity: each redaction
// type, and the entity a block names. Monitor-mode matches have no entity.
func TestHitsOfAReceipt(t *testing.T) {
	ts := int64(1_790_000_000_000)
	redacted := model.Receipt{ID: "rc1", TS: ts, KeyName: "helpdesk-prod", Team: "support", ResolvedModel: "gpt-5-mini", Verdict: "redacted", ContentCaptured: true,
		Redactions: []model.Redaction{{Type: "email", Count: 2}, {Type: "employee ID", Count: 1}},
		Rules:      []model.RuleEval{{Name: "no-pii-out", Version: 8, Matched: true, Action: "redact"}, {Name: "eu-only", Version: 1, Matched: false, Action: "no match"}, {Name: "watch", Version: 2, Matched: true, Action: "would block"}}}
	got := hitsOf(redacted)
	want := []DetectorHit{
		{ReceiptID: "rc1", TS: ts, Entity: "email", Action: "redacted", Count: 2, Rules: []string{"no-pii-out v8"}, KeyName: "helpdesk-prod", Team: "support", Model: "gpt-5-mini", ContentCaptured: true},
		{ReceiptID: "rc1", TS: ts, Entity: "employee ID", Action: "redacted", Count: 1, Rules: []string{"no-pii-out v8"}, KeyName: "helpdesk-prod", Team: "support", Model: "gpt-5-mini", ContentCaptured: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	blocked := model.Receipt{ID: "rc2", TS: ts, Verdict: "blocked", ErrorCode: "policy_blocked", ErrorDetail: `Rule block-src v3 matched entity "secret". Remove it from the prompt, or route through a self-hosted backend.`,
		Rules: []model.RuleEval{{Name: "block-src", Version: 3, Matched: true, Action: "block"}}}
	if got := hitsOf(blocked); !reflect.DeepEqual(got, []DetectorHit{{ReceiptID: "rc2", TS: ts, Entity: "secret", Action: "blocked", Count: 1, Rules: []string{"block-src v3"}}}) {
		t.Fatalf("blocked: %+v", got)
	}
	// A rule of several actions counts for each; a policy names its rule.
	multi := model.Receipt{ID: "rc4", TS: ts, Verdict: "redacted", Redactions: []model.Redaction{{Type: "email", Count: 1}},
		Rules: []model.RuleEval{{Name: "pii", Policy: "data", PolicyID: "p1", Version: 4, Matched: true, Action: "redact + route to"}}}
	if got := hitsOf(multi); len(got) != 1 || !reflect.DeepEqual(got[0].Rules, []string{"data/pii v4"}) {
		t.Fatalf("multi: %+v", got)
	}
	// Blocked on who, not on content: no entity, no hit.
	byTeam := model.Receipt{ID: "rc3", Verdict: "blocked", ErrorCode: "policy_blocked", ErrorDetail: "Rule no-web v1 blocks this request."}
	if got := hitsOf(byTeam); len(got) != 0 {
		t.Fatalf("by team: %+v", got)
	}
}

func TestHitQueueJoinsVerdictsAndFilters(t *testing.T) {
	hits := []DetectorHit{{ReceiptID: "rc1", Entity: "email"}, {ReceiptID: "rc1", Entity: "SSN"}, {ReceiptID: "rc2", Entity: "email"}}
	fp := store.DetectorVerdict{ReceiptID: "rc1", Entity: "email", Verdict: "false_positive", By: "dev@localhost", At: 5}
	verdicts := map[store.HitKey]store.DetectorVerdict{{ReceiptID: "rc1", Entity: "email"}: fp}
	all := hitQueue(hits, verdicts, "", false)
	if len(all) != 3 || all[0].Verdict == nil || *all[0].Verdict != fp || all[0].ETag != store.VerdictETag(&fp) || all[1].Verdict != nil || all[1].ETag != store.VerdictETag(nil) {
		t.Fatalf("all: %+v", all)
	}
	open := hitQueue(hits, verdicts, "email", true)
	if len(open) != 1 || open[0].ReceiptID != "rc2" {
		t.Fatalf("unreviewed email: %+v", open)
	}
}

// A custom entity can't be deleted while a policy's rule names it, live (any
// mode), in its draft, or in a published version a rollback could restore:
// the rule would silently stop matching.
func TestEntityInUseByPoliciesDraftsOrVersions(t *testing.T) {
	names := func(e string) []model.PolicyRule {
		return []model.PolicyRule{{Name: "r", When: []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"email", e}}}}}
	}
	policies := []model.Policy{
		{ID: "p1", Name: "no-pii-out", Mode: "enforce", Rules: names("SSN")},
		{ID: "p2", Name: "hr-only", Mode: "disabled", Rules: names("employee ID")},
		{ID: "p3", Name: "wip", Mode: "draft"},
		{ID: "p4", Name: "badges", Mode: "enforce", Rules: names("SSN")},
	}
	drafts := map[string]*store.PolicyDraft{"p3": {PolicyContent: store.PolicyContent{Name: "wip", Rules: names("employee ID")}}}
	versions := map[string][]store.PolicyVersion{
		"p4": {{Version: 3, PolicyContent: store.PolicyContent{Rules: names("SSN")}}, {Version: 2, PolicyContent: store.PolicyContent{Rules: names("employee ID")}}},
	}
	err := entityInUse("employee ID", policies, drafts, versions)
	want := `Policies hr-only, wip (draft) and badges (v2) name employee ID. Take it out of those policies first. A published version counts: rolling back to it would bring the entity back.`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if err := entityInUse("employee ID", policies[:2], drafts, versions); err == nil || err.Error() != `Policy hr-only names employee ID. Take it out of that policy first.` {
		t.Fatalf("one: %v", err)
	}
	if err := entityInUse("badge", policies, drafts, versions); err != nil {
		t.Fatal(err)
	}
	var c conflict
	if !errors.As(entityInUse("employee ID", policies, drafts, versions), &c) {
		t.Fatal("in use should be a 409")
	}
}
