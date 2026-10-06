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
	rules := []model.PolicyRule{
		{Name: "no-pii-out", Version: 8, Mode: "monitor", When: pii, Then: []model.Action{{Action: "redact"}}},
		{Name: "pii-block", Version: 2, Mode: "enforce", When: append(pii, model.Cond{Field: "team", Op: "is", Value: []string{"batch"}}), Then: []model.Action{{Action: "block"}}},
		// Not live: Warden skips them, so they don't use a detector.
		{Name: "off", Version: 3, Mode: "disabled", When: pii, Then: []model.Action{{Action: "block"}}},
		{Name: "wip", Version: 0, Mode: "draft", When: pii, Then: []model.Action{{Action: "block"}}},
		// "is" on a field isn't a detector, even if a value looks like one.
		{Name: "teams", Version: 1, Mode: "enforce", When: []model.Cond{{Field: "team", Op: "is", Value: []string{"secret"}}}, Then: []model.Action{{Action: "block"}}},
	}
	hits := map[string]store.DetectorHits{
		"email": {RedactedRequests: 40, RedactedMatches: 52, Blocked: 3},
		"phone": {RedactedRequests: 9}, // no detector by that name any more
	}
	verdicts := map[string]store.VerdictCounts{"email": {FalsePositives: 2, Confirmed: 5}}
	got := detectorViews(ds, nil, rules, hits, verdicts)
	want := []DetectorView{
		{DetectorInfo: ds[0], UsedBy: []DetectorUse{{"no-pii-out", 8, "monitor", "redact"}, {"pii-block", 2, "enforce", "block"}}},
		{DetectorInfo: ds[1], UsedBy: []DetectorUse{{"no-pii-out", 8, "monitor", "redact"}, {"pii-block", 2, "enforce", "block"}}, RedactedRequests24h: 40, RedactedMatches24h: 52, Blocked24h: 3,
			FalsePositives30d: 2, Confirmed30d: 5},
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

// A custom entity can't be deleted while a rule names it, live or in a
// draft: the rule would silently stop matching.
func TestEntityInUseByRulesOrDrafts(t *testing.T) {
	names := func(e string) model.Cond {
		return model.Cond{Field: "prompt", Op: "contains entity", Value: []string{"email", e}}
	}
	rules := []model.PolicyRule{
		{ID: "r1", Name: "no-pii-out", When: []model.Cond{names("SSN")}},
		{ID: "r2", Name: "hr-only", Mode: "disabled", When: []model.Cond{names("employee ID")}},
		{ID: "r3", Name: "wip"},
	}
	drafts := map[string]*store.RuleDraft{"r3": {RuleContent: store.RuleContent{Name: "wip", When: []model.Cond{names("employee ID")}}}}
	err := entityInUse("employee ID", rules, drafts)
	if err == nil || err.Error() != `Rules hr-only and wip (draft) name employee ID. Take it out of those rules first.` {
		t.Fatalf("got %v", err)
	}
	if err := entityInUse("badge", rules, drafts); err != nil {
		t.Fatal(err)
	}
	var c conflict
	if !errors.As(entityInUse("employee ID", rules, drafts), &c) {
		t.Fatal("in use should be a 409")
	}
}
