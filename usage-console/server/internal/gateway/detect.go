package gateway

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jbouder/stargate/server/internal/store"
)

// Entity detectors named the way policy rules refer to them ("contains
// entity"). These are simple regexes standing in for Warden's detectors.
type detector struct {
	re     *regexp.Regexp
	valid  func(string) bool
	label  string // placeholder written over a redacted match
	custom bool   // from the tenant's registry (store.CustomEntity)
	// sample is a made-up value this detector matches, and no other does:
	// replay puts it back where a placeholder stands (Unmask).
	sample string
}

var detectors = map[string]detector{
	"email":           {re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), label: "EMAIL", sample: "replay@example.com"},
	"SSN":             {re: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), label: "SSN", sample: "078-05-1120"},
	"phone":           {re: regexp.MustCompile(`\(\d{3}\) \d{3}-\d{4}|\b\d{3}-\d{3}-\d{4}\b`), label: "PHONE", sample: "(555) 010-0199"},
	"credit card":     {re: regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`), valid: luhn, label: "CARD", sample: "4111 1111 1111 1111"},
	"secret":          {re: regexp.MustCompile(`\b(?:sk-[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{36})\b`), label: "SECRET", sample: "sk-REPLAYSAMPLE0000000000"},
	"private key":     {re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), label: "PRIVATE_KEY", sample: "-----BEGIN PRIVATE KEY-----"},
	"source code":     {re: regexp.MustCompile("(?m)^```[a-z]*\\n(?:.*\\n)*?```"), label: "CODE", sample: "\n```\nsample()\n```\n"},
	"Acme account ID": {re: regexp.MustCompile(`\bACME-\d{8}\b`), label: "ACCOUNT", sample: "ACME-00000000"},
}

func luhn(s string) bool {
	sum, n := 0, 0
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if n%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		n++
	}
	return n >= 13 && sum%10 == 0
}

// Detectors is the entity detectors one snapshot enforces: the built-ins
// plus the tenant's custom entities (§5.3's registry). A nil *Detectors is
// the built-ins alone.
type Detectors struct{ byName map[string]detector }

// NewDetectors is the built-ins plus custom. An entity that fails the
// pattern check (validated on write, so only if the rules changed since) is
// left out and named in the error, rather than failing the whole snapshot.
func NewDetectors(custom []store.CustomEntity) (*Detectors, error) {
	d := &Detectors{byName: maps.Clone(detectors)}
	var errs []error
	for _, e := range custom {
		if _, builtin := detectors[e.Name]; builtin {
			errs = append(errs, fmt.Errorf("custom entity %s: a built-in has that name", e.Name))
			continue
		}
		if err := CheckPattern(e.Pattern); err != nil {
			errs = append(errs, fmt.Errorf("custom entity %s: %w", e.Name, err))
			continue
		}
		var sample string
		if len(e.MustMatch) > 0 {
			sample = e.MustMatch[0]
		}
		d.byName[e.Name] = detector{re: regexp.MustCompile(e.Pattern), label: e.Label, custom: true, sample: sample}
	}
	return d, errors.Join(errs...)
}

func (d *Detectors) all() map[string]detector {
	if d == nil {
		return detectors
	}
	return d.byName
}

// find returns how many times an entity appears in text.
func (d *Detectors) find(entity, text string) int {
	det, ok := d.all()[entity]
	if !ok {
		return 0
	}
	n := 0
	for _, m := range det.re.FindAllString(text, -1) {
		if det.valid == nil || det.valid(m) {
			n++
		}
	}
	return n
}

// redact replaces every valid match with the placeholder name gives it
// (placeholders.For: "[EMAIL_1]" and so on).
func (d *Detectors) redact(entity, text string, name func(label, match string) string) string {
	det, ok := d.all()[entity]
	if !ok {
		return text
	}
	return det.re.ReplaceAllStringFunc(text, func(m string) string {
		if det.valid != nil && !det.valid(m) {
			return m
		}
		return name(det.label, m)
	})
}

// exfil flags markdown images or links that smuggle data out in a query
// string: the response-inspection check that truncates a stream.
var exfil = regexp.MustCompile(`!\[[^\]]*\]\(https?://[^)\s]*\?[^)\s]*\)`)

// DetectorInfo describes a detector for the console: how it matches, and
// what a redaction writes in place of the first match.
type DetectorInfo struct {
	Entity      string `json:"entity"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	Placeholder string `json:"placeholder"`
	Custom      bool   `json:"custom"`
}

// List is every detector, in Entities order.
func (d *Detectors) List() []DetectorInfo {
	all := d.all()
	out := make([]DetectorInfo, 0, len(all))
	for _, e := range d.Entities() {
		det := all[e]
		kind := "regex"
		switch {
		case det.custom:
			kind = "custom regex"
		case det.valid != nil:
			kind = "regex + Luhn check"
		}
		out = append(out, DetectorInfo{Entity: e, Kind: kind, Pattern: det.re.String(), Placeholder: "[" + det.label + "_1]", Custom: det.custom})
	}
	return out
}

// Entities is the entity types rules can name in "contains entity".
func (d *Detectors) Entities() []string {
	return slices.Sorted(maps.Keys(d.all()))
}

// DetectorList is the built-in detectors, in Entities order.
func DetectorList() []DetectorInfo { return (*Detectors)(nil).List() }

// Entities is the built-in entity types.
func Entities() []string { return (*Detectors)(nil).Entities() }

// ---- custom entities -------------------------------------------------------

// Limits on a custom pattern. Go's regexp is RE2: matching is linear in the
// prompt's length, with no backtracking, so no pattern can be catastrophic.
// What's left to bound is the pattern itself, since its compiled size
// multiplies the per-byte cost on every prompt Warden checks.
const (
	PatternMaxLen  = 512  // characters of pattern
	PatternMaxInst = 1000 // instructions in the compiled program
	ExampleMax     = 20   // examples of each kind
	ExampleMaxLen  = 500  // characters per example
	EntityNameMax  = 40
)

// CheckPattern says whether p is safe to run on every prompt: it compiles
// as RE2, is bounded in size, and can't match empty text (a match on
// nothing would redact nothing, or block everything).
func CheckPattern(p string) error {
	switch {
	case p == "":
		return errors.New("pattern is required")
	case utf8.RuneCountInString(p) > PatternMaxLen:
		return fmt.Errorf("pattern is at most %d characters", PatternMaxLen)
	}
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		var se *syntax.Error
		if errors.As(err, &se) && (se.Code == syntax.ErrLarge || se.Code == syntax.ErrInvalidRepeatSize || se.Code == syntax.ErrNestingDepth) {
			return fmt.Errorf("pattern is too large: %v", err)
		}
		return fmt.Errorf("pattern doesn't compile (Go RE2 syntax: no backreferences or lookaround): %v", err)
	}
	if nullable(re) {
		return errors.New("pattern can match empty text: it must match at least one character")
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return fmt.Errorf("pattern doesn't compile: %v", err)
	}
	if len(prog.Inst) > PatternMaxInst {
		return fmt.Errorf("pattern is too large: %d steps compiled, at most %d", len(prog.Inst), PatternMaxInst)
	}
	if _, err := regexp.Compile(p); err != nil {
		return fmt.Errorf("pattern doesn't compile: %v", err)
	}
	return nil
}

// nullable reports whether re can match without consuming a character.
// Anchors and word boundaries match empty text, so a pattern of only those
// counts as nullable too.
func nullable(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpNoMatch, syntax.OpCharClass, syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return false
	case syntax.OpLiteral:
		return len(re.Rune) == 0
	case syntax.OpCapture, syntax.OpPlus:
		return nullable(re.Sub[0])
	case syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpRepeat:
		return re.Min == 0 || nullable(re.Sub[0])
	case syntax.OpConcat:
		for _, s := range re.Sub {
			if !nullable(s) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		return slices.ContainsFunc(re.Sub, nullable)
	}
	// OpEmptyMatch, line and text anchors, word boundaries.
	return true
}

var (
	entityName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]*$`)
	entityLabel = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,31}$`)
)

// ValidateCustomEntity checks e before it's saved: its name (unique among
// built-ins and the other custom entities, ignoring case), its label (not
// another detector's, so a placeholder names one entity), its pattern
// (CheckPattern), and that the pattern matches every must-match example and
// none of the must-not-match ones. others is the tenant's custom entities;
// e's own row among them (same ID) is skipped.
func ValidateCustomEntity(e store.CustomEntity, others []store.CustomEntity) error {
	name := strings.TrimSpace(e.Name)
	switch {
	case name == "":
		return errors.New("name is required")
	case utf8.RuneCountInString(name) > EntityNameMax:
		return fmt.Errorf("name is at most %d characters", EntityNameMax)
	case !entityName.MatchString(name):
		return errors.New("name may use letters, digits, spaces, dashes and underscores")
	case !entityLabel.MatchString(e.Label):
		return errors.New("label must be capital letters, digits and underscores")
	}
	for _, b := range Entities() {
		if strings.EqualFold(b, name) {
			return fmt.Errorf("name %q is a built-in detector", name)
		}
		if detectors[b].label == e.Label {
			return fmt.Errorf("label %s is used by %s", e.Label, b)
		}
	}
	for _, o := range others {
		if o.ID == e.ID {
			continue
		}
		if strings.EqualFold(o.Name, name) {
			return fmt.Errorf("name %q is taken by another custom entity", name)
		}
		if o.Label == e.Label {
			return fmt.Errorf("label %s is used by %s", e.Label, o.Name)
		}
	}
	if err := CheckPattern(e.Pattern); err != nil {
		return err
	}
	if len(e.MustMatch) > ExampleMax || len(e.MustNotMatch) > ExampleMax {
		return fmt.Errorf("at most %d examples of each kind", ExampleMax)
	}
	re := regexp.MustCompile(e.Pattern)
	for _, x := range slices.Concat(e.MustMatch, e.MustNotMatch) {
		if utf8.RuneCountInString(x) > ExampleMaxLen {
			return fmt.Errorf("an example is at most %d characters", ExampleMaxLen)
		}
	}
	for _, x := range e.MustMatch {
		if !re.MatchString(x) {
			return fmt.Errorf("pattern doesn't match %q, which it must", x)
		}
	}
	for _, x := range e.MustNotMatch {
		if re.MatchString(x) {
			return fmt.Errorf("pattern matches %q, which it mustn't", x)
		}
	}
	return nil
}

// SampleMatches is what pattern finds in sample, and the sample as a redact
// would leave it, for trying a pattern before saving it. The pattern must
// pass CheckPattern first.
func SampleMatches(pattern, label, sample string) ([]string, string) {
	re := regexp.MustCompile(pattern)
	names := newPlaceholders(sample)
	found := re.FindAllString(sample, -1)
	if found == nil {
		found = []string{}
	}
	return found, re.ReplaceAllStringFunc(sample, func(m string) string { return names.For(label, m) })
}
