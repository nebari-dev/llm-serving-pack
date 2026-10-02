package api

import (
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
	got := detectorViews(ds, rules, hits)
	want := []DetectorView{
		{DetectorInfo: ds[0], UsedBy: []DetectorUse{{"no-pii-out", 8, "monitor", "redact"}, {"pii-block", 2, "enforce", "block"}}},
		{DetectorInfo: ds[1], UsedBy: []DetectorUse{{"no-pii-out", 8, "monitor", "redact"}, {"pii-block", 2, "enforce", "block"}}, RedactedRequests24h: 40, RedactedMatches24h: 52, Blocked24h: 3},
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
