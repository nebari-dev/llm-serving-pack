package gateway

import (
	"strconv"
	"strings"
)

// StepRehydrated is the trace step Warden adds once a response's placeholders
// are restored; receipt-ingest puts it after the upstream call.
const StepRehydrated = "Placeholders rehydrated"

// RehydrateOnReturn is what a redact action's detail says when its rule wants
// the values back in the response (§5.3 "rehydrate": true). Anything else,
// "no rehydrate" included, leaves the placeholders in.
const RehydrateOnReturn = "rehydrate on return"

// placeholders numbers a request's redactions: one counter per label across
// every message, and the same value always gets the same placeholder, so the
// model sees one person where the caller wrote one (§4.5 step 5: stable
// placeholder tokens). A placeholder the caller's own text already contains is
// skipped, so restoring ours never rewrites theirs.
type placeholders struct {
	byValue map[string]string // label + value → placeholder
	next    map[string]int
	taken   string
}

func newPlaceholders(text string) *placeholders {
	return &placeholders{byValue: map[string]string{}, next: map[string]int{}, taken: text}
}

func (p *placeholders) For(label, value string) string {
	k := label + "\x00" + value
	if ph, ok := p.byValue[k]; ok {
		return ph
	}
	for {
		p.next[label]++
		ph := "[" + label + "_" + strconv.Itoa(p.next[label]) + "]"
		if !strings.Contains(p.taken, ph) {
			p.byValue[k] = ph
			return ph
		}
	}
}

// Vault holds the values one request's redactions replaced, for rules that
// rehydrate on return. It lives as long as the request: Warden keeps it on
// the request's ext_proc stream, and nothing in it is stored or logged.
// Not safe for concurrent use.
type Vault struct {
	values   map[string]vaultEntry // placeholder → what it replaced
	restored map[string]int        // entity → placeholders restored
}

type vaultEntry struct{ value, entity string }

func (v *Vault) put(placeholder, value, entity string) {
	if v.values == nil {
		v.values = map[string]vaultEntry{}
	}
	v.values[placeholder] = vaultEntry{value, entity}
}

// Len is how many placeholders the vault can restore.
func (v *Vault) Len() int {
	if v == nil {
		return 0
	}
	return len(v.values)
}

// Restored counts the placeholders restored so far, by entity.
func (v *Vault) Restored() map[string]int {
	if v == nil || v.restored == nil {
		return map[string]int{}
	}
	return v.restored
}

// Swap restores every placeholder in a whole text.
func (v *Vault) Swap(s string) string {
	r := v.Rehydrator()
	return r.Feed(s) + r.Flush()
}

// Rehydrator restores placeholders in text that arrives in pieces, such as
// a streamed reply's deltas: one per stream of text.
func (v *Vault) Rehydrator() *Rehydrator { return &Rehydrator{v: v} }

// Rehydrator holds back a tail that could still become a placeholder, and
// releases it once the next piece shows whether it is one.
type Rehydrator struct {
	v    *Vault
	held string
}

// Feed returns the text that can go out now, placeholders restored.
func (r *Rehydrator) Feed(s string) string {
	buf := r.held + s
	r.held = ""
	if r.v.Len() == 0 {
		return buf
	}
	var out strings.Builder
	for {
		i := strings.IndexByte(buf, '[')
		if i < 0 {
			out.WriteString(buf)
			return out.String()
		}
		out.WriteString(buf[:i])
		buf = buf[i:]
		if ph, e, ok := r.v.at(buf); ok {
			out.WriteString(e.value)
			if r.v.restored == nil {
				r.v.restored = map[string]int{}
			}
			r.v.restored[e.entity]++
			buf = buf[len(ph):]
			continue
		}
		if r.v.startsOne(buf) {
			r.held = buf
			return out.String()
		}
		out.WriteByte('[')
		buf = buf[1:]
	}
}

// Flush returns whatever is still held: the text ended before it became a
// placeholder, so it goes out as written.
func (r *Rehydrator) Flush() string {
	s := r.held
	r.held = ""
	return s
}

// at reports the placeholder s starts with, if any.
func (v *Vault) at(s string) (string, vaultEntry, bool) {
	if j := strings.IndexByte(s, ']'); j > 0 {
		if e, ok := v.values[s[:j+1]]; ok {
			return s[:j+1], e, true
		}
	}
	return "", vaultEntry{}, false
}

// startsOne reports whether s is the start of a placeholder, cut short.
func (v *Vault) startsOne(s string) bool {
	if strings.IndexByte(s, ']') >= 0 {
		return false
	}
	for ph := range v.values {
		if len(s) < len(ph) && strings.HasPrefix(ph, s) {
			return true
		}
	}
	return false
}
