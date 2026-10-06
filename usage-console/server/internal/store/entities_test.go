package store

import "testing"

// An entity's etag moves with anything an edit can change, so a stale edit
// or delete is refused (§6).
func TestCustomEntityETagMovesWithEachField(t *testing.T) {
	base := CustomEntity{ID: "ce1", Name: "employee ID", Pattern: `EMP-\d{6}`, Label: "EMPLOYEE", MustMatch: []string{"EMP-000001"}, MustNotMatch: []string{}}
	seen := map[string]bool{CustomEntityETag(base): true}
	for _, edit := range []func(*CustomEntity){
		func(e *CustomEntity) { e.Pattern = `EMP-\d{7}` },
		func(e *CustomEntity) { e.Label = "STAFF" },
		func(e *CustomEntity) { e.MustMatch = []string{"EMP-000002"} },
		func(e *CustomEntity) { e.MustNotMatch = []string{"EMP-1"} },
	} {
		e := base
		edit(&e)
		tag := CustomEntityETag(e)
		if seen[tag] {
			t.Fatalf("etag didn't move for %+v", e)
		}
		seen[tag] = true
	}
	// Who saved it and when don't change what it matches.
	e := base
	e.UpdatedBy, e.UpdatedAt = "someone", 9
	if CustomEntityETag(e) != CustomEntityETag(base) {
		t.Fatal("etag should cover content only")
	}
}

// A hit with no verdict has an etag too, so the first verdict is checked
// against "nobody has reviewed this yet".
func TestVerdictETag(t *testing.T) {
	fp := DetectorVerdict{ReceiptID: "rc1", Entity: "email", Verdict: "false_positive", By: "a", At: 1}
	ok := fp
	ok.Verdict = "confirmed"
	if VerdictETag(nil) == VerdictETag(&fp) || VerdictETag(&fp) == VerdictETag(&ok) {
		t.Fatal("etags should differ")
	}
}
