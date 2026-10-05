package gateway

import (
	"regexp"
	"slices"
)

// Entity detectors named the way policy rules refer to them ("contains
// entity"). These are simple regexes standing in for Warden's detectors.
type detector struct {
	re    *regexp.Regexp
	valid func(string) bool
	label string // placeholder written over a redacted match
}

var detectors = map[string]detector{
	"email":           {re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), label: "EMAIL"},
	"SSN":             {re: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), label: "SSN"},
	"phone":           {re: regexp.MustCompile(`\(\d{3}\) \d{3}-\d{4}|\b\d{3}-\d{3}-\d{4}\b`), label: "PHONE"},
	"credit card":     {re: regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`), valid: luhn, label: "CARD"},
	"secret":          {re: regexp.MustCompile(`\b(?:sk-[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{36})\b`), label: "SECRET"},
	"private key":     {re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), label: "PRIVATE_KEY"},
	"source code":     {re: regexp.MustCompile("(?m)^```[a-z]*\\n(?:.*\\n)*?```"), label: "CODE"},
	"Acme account ID": {re: regexp.MustCompile(`\bACME-\d{8}\b`), label: "ACCOUNT"},
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

// find returns how many times an entity appears in text.
func find(entity, text string) int {
	d, ok := detectors[entity]
	if !ok {
		return 0
	}
	n := 0
	for _, m := range d.re.FindAllString(text, -1) {
		if d.valid == nil || d.valid(m) {
			n++
		}
	}
	return n
}

// redact replaces every valid match with the placeholder name gives it
// (placeholders.For: "[EMAIL_1]" and so on).
func redact(entity, text string, name func(label, match string) string) string {
	d, ok := detectors[entity]
	if !ok {
		return text
	}
	return d.re.ReplaceAllStringFunc(text, func(m string) string {
		if d.valid != nil && !d.valid(m) {
			return m
		}
		return name(d.label, m)
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
}

// DetectorList is every detector, in Entities order.
func DetectorList() []DetectorInfo {
	out := make([]DetectorInfo, 0, len(detectors))
	for _, e := range Entities() {
		d := detectors[e]
		kind := "regex"
		if d.valid != nil {
			kind = "regex + Luhn check"
		}
		out = append(out, DetectorInfo{Entity: e, Kind: kind, Pattern: d.re.String(), Placeholder: "[" + d.label + "_1]"})
	}
	return out
}

// Entities is the entity types rules can name in "contains entity".
func Entities() []string {
	out := make([]string, 0, len(detectors))
	for e := range detectors {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}
