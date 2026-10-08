package gateway

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// A custom pattern runs on every prompt Warden sees, so it must be safe to
// run as well as correct: RE2 (linear time, no backtracking), a bounded
// size, and never a match on nothing.
func TestCheckPatternRefusesUnsafePatterns(t *testing.T) {
	for p, want := range map[string]string{
		`\bEMP-\d{6}\b`:                      "",
		`(?i)project\s+[a-z]+`:               "",
		`ACME-\d{8}`:                         "",
		``:                                   "pattern is required",
		strings.Repeat("a", PatternMaxLen+1): "pattern is at most 512 characters",
		`(a`:                                 "pattern doesn't compile",
		`(\w+)\1`:                            "pattern doesn't compile",
		`foo(?=bar)`:                         "pattern doesn't compile",
		`a*`:                                 "pattern can match empty text",
		`\d?`:                                "pattern can match empty text",
		`^`:                                  "pattern can match empty text",
		`\b`:                                 "pattern can match empty text",
		`x|`:                                 "pattern can match empty text",
		`(?:ab)*`:                            "pattern can match empty text",
		`[a-z]{0,3}`:                         "pattern can match empty text",
		`(?:[a-z]{1,100}){1,100}`:            "pattern is too large",
	} {
		got := ""
		if err := CheckPattern(p); err != nil {
			got = err.Error()
		}
		if want == "" && got != "" || want != "" && !strings.HasPrefix(got, want) {
			t.Errorf("CheckPattern(%.40q) = %q, want %q", p, got, want)
		}
	}
}

// The built-ins meet the bar a custom pattern must, so the limits are ones
// real detectors live within.
func TestBuiltinPatternsPassTheSafetyCheck(t *testing.T) {
	for _, d := range DetectorList() {
		if err := CheckPattern(d.Pattern); err != nil {
			t.Errorf("%s: %v", d.Entity, err)
		}
	}
}

func employee() store.CustomEntity {
	return store.CustomEntity{ID: "ce1", Name: "employee ID", Pattern: `\bEMP-\d{6}\b`, Label: "EMPLOYEE",
		MustMatch: []string{"badge EMP-004211 at the door"}, MustNotMatch: []string{"EMP-42", "TEMP-004211"}}
}

func TestValidateCustomEntity(t *testing.T) {
	other := store.CustomEntity{ID: "ce2", Name: "Project code", Pattern: `\bPRJ-\d{4}\b`, Label: "PROJECT"}
	for name, c := range map[string]struct {
		edit func(*store.CustomEntity)
		want string
	}{
		"ok":                 {func(*store.CustomEntity) {}, ""},
		"same entity again":  {func(e *store.CustomEntity) { e.ID = "ce1" }, ""},
		"no name":            {func(e *store.CustomEntity) { e.Name = " " }, "name is required"},
		"long name":          {func(e *store.CustomEntity) { e.Name = strings.Repeat("a", 41) }, "name is at most 40 characters"},
		"slash in name":      {func(e *store.CustomEntity) { e.Name = "a/b" }, "name may use letters, digits, spaces, dashes and underscores"},
		"built-in name":      {func(e *store.CustomEntity) { e.Name = "Email" }, `name "Email" is a built-in detector`},
		"taken name":         {func(e *store.CustomEntity) { e.Name = "project CODE" }, `name "project CODE" is taken by another custom entity`},
		"lowercase label":    {func(e *store.CustomEntity) { e.Label = "employee" }, "label must be capital letters, digits and underscores"},
		"built-in label":     {func(e *store.CustomEntity) { e.Label = "EMAIL" }, `label EMAIL is used by email`},
		"taken label":        {func(e *store.CustomEntity) { e.Label = "PROJECT" }, `label PROJECT is used by Project code`},
		"bad pattern":        {func(e *store.CustomEntity) { e.Pattern = `.*` }, "pattern can match empty text: it must match at least one character"},
		"misses an example":  {func(e *store.CustomEntity) { e.MustMatch = append(e.MustMatch, "EMP 004211") }, `pattern doesn't match "EMP 004211", which it must`},
		"hits a non-example": {func(e *store.CustomEntity) { e.MustNotMatch = append(e.MustNotMatch, "id EMP-123456") }, `pattern matches "id EMP-123456", which it mustn't`},
		"too many examples":  {func(e *store.CustomEntity) { e.MustMatch = slices.Repeat([]string{"EMP-004211"}, 21) }, "at most 20 examples of each kind"},
		"long example":       {func(e *store.CustomEntity) { e.MustMatch = []string{"EMP-004211 " + strings.Repeat("x", 500)} }, "an example is at most 500 characters"},
	} {
		e := employee()
		if name == "same entity again" {
			// Editing an entity doesn't collide with itself.
			other := e
			c.edit(&e)
			if err := ValidateCustomEntity(e, []store.CustomEntity{other}); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		c.edit(&e)
		got := ""
		if err := ValidateCustomEntity(e, []store.CustomEntity{other}); err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestDetectorsIncludeCustomEntities(t *testing.T) {
	d, err := NewDetectors([]store.CustomEntity{employee()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.Entities(), "employee ID") || !slices.Contains(d.Entities(), "email") {
		t.Fatalf("entities %v", d.Entities())
	}
	if slices.Contains(Entities(), "employee ID") {
		t.Fatal("a custom entity leaked into the built-ins")
	}
	i := slices.IndexFunc(d.List(), func(x DetectorInfo) bool { return x.Entity == "employee ID" })
	if i < 0 {
		t.Fatalf("list %+v", d.List())
	}
	if got := d.List()[i]; got != (DetectorInfo{Entity: "employee ID", Kind: "custom regex", Pattern: `\bEMP-\d{6}\b`, Placeholder: "[EMPLOYEE_1]", Custom: true}) {
		t.Fatalf("got %+v", got)
	}
	if n := d.find("employee ID", "EMP-000001 and EMP-000002, not EMP-1"); n != 2 {
		t.Fatalf("found %d", n)
	}
	// A nil registry is the built-ins.
	var none *Detectors
	if none.find("employee ID", "EMP-000001") != 0 || none.find("email", "a@b.com") != 1 {
		t.Fatal("nil registry should be the built-ins only")
	}
}

// A stored entity that no longer compiles is left out, and said so, rather
// than failing the whole snapshot (and with it every other rule).
func TestNewDetectorsSkipsAnInvalidEntity(t *testing.T) {
	bad := employee()
	bad.ID, bad.Name, bad.Pattern = "ce9", "broken", `(`
	d, err := NewDetectors([]store.CustomEntity{employee(), bad})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("err %v", err)
	}
	if !slices.Contains(d.Entities(), "employee ID") || slices.Contains(d.Entities(), "broken") {
		t.Fatalf("entities %v", d.Entities())
	}
}

// The engine reads the snapshot's registry: a rule naming a custom entity
// redacts it before the request leaves, with the entity's own placeholder.
func TestCustomEntityRedactedByRule(t *testing.T) {
	s := DemoSnapshot()
	var err error
	if s.Detectors, err = NewDetectors([]store.CustomEntity{employee()}); err != nil {
		t.Fatal(err)
	}
	s.Policies = append([]model.Policy{policy("no-employee-ids", "enforce", "closed", 1,
		rule("no-employee-ids", []model.Cond{{Field: "prompt", Op: "contains entity", Value: []string{"employee ID"}}}, model.Action{Action: "redact", Detail: "employee ID"}))}, s.Policies...)
	up := &fixedUp{}
	in := Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "badge EMP-004211 left the building"), Now: demoNow}
	d := Admit(s, in, rand.New(rand.NewPCG(1, 2)))
	if d.Reject != nil {
		t.Fatalf("rejected %+v", d.Reject)
	}
	if got := d.Req.Messages[0].Content; got != "badge [EMPLOYEE_1] left the building" {
		t.Fatalf("prompt %q", got)
	}
	rc := run(t, s, Input{Secret: secret("k1"), Req: chat("gpt-5-mini", "badge EMP-004211 left the building")}, up)
	if rc.Verdict != "redacted" || !slices.Contains(rc.Redactions, model.Redaction{Type: "employee ID", Count: 1}) {
		t.Fatalf("got %s %+v", rc.Verdict, rc.Redactions)
	}
}

// The built-ins are general kinds of data. A company's own identifiers (the
// demo's "Acme account ID") are custom entities, made on the Detectors tab.
func TestBuiltInsAreGeneralNotOneCompanys(t *testing.T) {
	want := []string{"SSN", "credit card", "email", "phone", "private key", "secret", "source code"}
	if got := Entities(); !slices.Equal(got, want) {
		t.Fatalf("built-ins = %v, want %v", got, want)
	}
}
