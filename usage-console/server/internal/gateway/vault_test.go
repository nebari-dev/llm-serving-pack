package gateway

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/fakellm"
)

func admitMsgs(t *testing.T, s *Snapshot, key string, msgs ...string) *Decision {
	t.Helper()
	req := fakellm.ChatRequest{Model: "gpt-5-mini"}
	for _, m := range msgs {
		req.Messages = append(req.Messages, fakellm.Message{Role: "user", Content: m})
	}
	d := Admit(s, Input{Secret: secret(key), Req: req, Now: demoNow}, rand.New(rand.NewPCG(1, 2)))
	if d.Reject != nil {
		t.Fatalf("rejected: %+v", d.Reject)
	}
	return d
}

// The same value gets the same placeholder across messages, so the model sees
// one person, and the vault restores it once.
func TestPlaceholdersAreStableAcrossTheRequest(t *testing.T) {
	d := admitMsgs(t, DemoSnapshot(), "k1", "mail a@b.com", "cc c@d.org and a@b.com")
	if got := d.Req.Messages[0].Content; got != "mail [EMAIL_1]" {
		t.Fatalf("message 0 = %q", got)
	}
	if got := d.Req.Messages[1].Content; got != "cc [EMAIL_2] and [EMAIL_1]" {
		t.Fatalf("message 1 = %q", got)
	}
	if d.Vault.Len() != 2 {
		t.Fatalf("vault holds %d", d.Vault.Len())
	}
	if got := d.Vault.Swap("to [EMAIL_1], [EMAIL_2]"); got != "to a@b.com, c@d.org" {
		t.Fatalf("swap = %q", got)
	}
}

// A placeholder the caller typed isn't one of ours: ours skips past it, so
// the caller's own text comes back as written.
func TestPlaceholderSkipsOneThePromptAlreadyHas(t *testing.T) {
	d := admitMsgs(t, DemoSnapshot(), "k1", "literal [EMAIL_1] and a@b.com")
	if got := d.Req.Messages[0].Content; got != "literal [EMAIL_1] and [EMAIL_2]" {
		t.Fatalf("redacted = %q", got)
	}
	if got := d.Vault.Swap("[EMAIL_1] [EMAIL_2]"); got != "[EMAIL_1] a@b.com" {
		t.Fatalf("swap = %q", got)
	}
}

// Only a rule whose action says "rehydrate on return" fills the vault; "no
// rehydrate" (r4) keeps its placeholders in the response.
func TestOnlyRehydratingRulesFillTheVault(t *testing.T) {
	s := DemoSnapshot()
	for i := range s.Rules {
		if s.Rules[i].ID == "r4" {
			s.Rules[i].Mode = "enforce"
		}
	}
	d := admitMsgs(t, s, "k1", "card 4111 1111 1111 1111, mail a@b.com")
	if c := d.Req.Messages[0].Content; strings.Contains(c, "4111") || strings.Contains(c, "@") {
		t.Fatalf("not redacted: %q", c)
	}
	if d.Vault.Len() != 1 {
		t.Fatalf("vault holds %d, want only the email", d.Vault.Len())
	}
	if got := d.Vault.Swap("[CARD_1] [EMAIL_1]"); got != "[CARD_1] a@b.com" {
		t.Fatalf("swap = %q", got)
	}
	if got := d.Vault.Restored(); got["email"] != 1 || got["credit card"] != 0 {
		t.Fatalf("restored = %v", got)
	}
}

func TestNoRedactionLeavesTheVaultEmpty(t *testing.T) {
	d := admitMsgs(t, DemoSnapshot(), "k1", "hello")
	if d.Vault.Len() != 0 {
		t.Fatalf("vault holds %d", d.Vault.Len())
	}
	if got := d.Vault.Swap("[EMAIL_1]"); got != "[EMAIL_1]" {
		t.Fatalf("swap = %q", got)
	}
}

func vaultOf(pairs ...string) *Vault {
	v := &Vault{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.put(pairs[i], pairs[i+1], "email")
	}
	return v
}

// A placeholder split across pieces is held back until it's whole.
func TestRehydratorJoinsASplitPlaceholder(t *testing.T) {
	r := vaultOf("[EMAIL_1]", "a@b.com").Rehydrator()
	var out []string
	for _, p := range []string{"write to [EM", "AIL", "_1", "] today"} {
		out = append(out, r.Feed(p))
	}
	out = append(out, r.Flush())
	if got := strings.Join(out, "|"); got != "write to |||a@b.com today|" {
		t.Fatalf("pieces = %q", got)
	}
}

// Text that only looks like the start of a placeholder goes out as soon as it
// can't be one, and a tail still held at the end is flushed as is.
func TestRehydratorReleasesWhatIsNotAPlaceholder(t *testing.T) {
	v := vaultOf("[EMAIL_1]", "a@b.com")
	r := v.Rehydrator()
	if got := r.Feed("a [list] of [E"); got != "a [list] of " {
		t.Fatalf("feed = %q", got)
	}
	if got := r.Feed("X] and [EMAIL_9] and [EMAIL_"); got != "[EX] and [EMAIL_9] and " {
		t.Fatalf("feed = %q", got)
	}
	if got := r.Flush(); got != "[EMAIL_" {
		t.Fatalf("flush = %q", got)
	}
	if v.Restored()["email"] != 0 {
		t.Fatalf("restored = %v", v.Restored())
	}
}

// A restored value is output, not input: brackets in it aren't scanned again.
// "[EMAIL_1" is a prefix of "[EMAIL_10]", so it waits for the next piece.
func TestRehydratorDoesNotRescanValues(t *testing.T) {
	v := vaultOf("[CODE_1]", "x = a[EMAIL_1]", "[EMAIL_1]", "a@b.com", "[EMAIL_10]", "j@k.io")
	r := v.Rehydrator()
	if got := r.Feed("[CODE_1] [EMAIL_1"); got != "x = a[EMAIL_1] " {
		t.Fatalf("feed = %q", got)
	}
	if got := r.Feed("0]"); got != "j@k.io" {
		t.Fatalf("feed = %q", got)
	}
	if got := v.Restored()["email"]; got != 2 {
		t.Fatalf("restored = %d", got)
	}
}
