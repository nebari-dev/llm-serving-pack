package secrets

import (
	"fmt"
	"hash/fnv"
	"testing"
)

// expectedSanitized mirrors the sanitization rules in sanitizeUsernameForKey
// so test cases can pin exact expected values without duplicating the hash
// computation in each row. It is intentionally a separate implementation
// from the production code so that an accidental change to the sanitization
// rules will fail these tests.
func expectedSanitized(raw string) string {
	if raw == "" {
		return ""
	}
	allValid := true
	for i := 0; i < len(raw); i++ {
		if !isValidDataKeyByte(raw[i]) {
			allValid = false
			break
		}
	}
	if allValid {
		return raw
	}
	out := make([]byte, 0, len(raw)+9)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '@':
			out = append(out, "-at-"...)
		case isValidDataKeyByte(c):
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(raw))
	return fmt.Sprintf("%s-%08x", string(out), h.Sum32())
}

func TestSanitizeUsernameForKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty input returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "plain alphanumeric is unchanged",
			input: "chuck",
			want:  "chuck",
		},
		{
			name:  "alphanumeric with allowed punctuation is unchanged",
			input: "chuck.mc-andrew_1",
			want:  "chuck.mc-andrew_1",
		},
		{
			name:  "single @ becomes -at- with hash suffix",
			input: "alice@example.com",
			want:  expectedSanitized("alice@example.com"),
		},
		{
			name:  "multiple @ each become -at- with hash suffix",
			input: "weird@@example.com",
			want:  expectedSanitized("weird@@example.com"),
		},
		{
			name:  "plus is replaced with dash",
			input: "alice+tag@example.com",
			want:  expectedSanitized("alice+tag@example.com"),
		},
		{
			name:  "space is replaced with dash",
			input: "alice smith",
			want:  expectedSanitized("alice smith"),
		},
		{
			name:  "unicode bytes are each replaced with dash",
			input: "alicé",
			want:  expectedSanitized("alicé"),
		},
		{
			name:  "uppercase is preserved",
			input: "Alice",
			want:  "Alice",
		},
		{
			name:  "digits are preserved",
			input: "user123",
			want:  "user123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeUsernameForKey(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeUsernameForKey(%q) = %q, want %q", tc.input, got, tc.want)
			}
			// The result must itself be a valid k8s data key name, except
			// for the empty input case which the caller is expected to
			// reject before reaching the API server.
			if tc.input != "" && !isValidDataKeyName(got) {
				t.Errorf("sanitizeUsernameForKey(%q) = %q is not a valid k8s data key", tc.input, got)
			}
			if tc.input == "" && got != "" {
				t.Errorf("sanitizeUsernameForKey(\"\") = %q, want \"\"", got)
			}
		})
	}
}

// TestSanitizeUsernameForKey_NoCollisions exercises the collision case the
// hash suffix was added to defend against: two distinct raw usernames must
// never produce the same sanitized form even when one of them happens to
// contain the literal "-at-" sequence the sanitizer emits.
func TestSanitizeUsernameForKey_NoCollisions(t *testing.T) {
	pairs := []struct {
		a, b string
	}{
		{"alice@example.com", "alice-at-example.com"},
		{"alice@example.com", "alice@@example.com"},
		{"alice+bob@example.com", "alice-bob-at-example.com"},
		{"alicé", "alic--"}, // two-byte unicode vs literal two dashes
	}
	for _, p := range pairs {
		t.Run(p.a+" vs "+p.b, func(t *testing.T) {
			gotA := sanitizeUsernameForKey(p.a)
			gotB := sanitizeUsernameForKey(p.b)
			if gotA == gotB {
				t.Errorf("collision: sanitizeUsernameForKey(%q) == sanitizeUsernameForKey(%q) == %q", p.a, p.b, gotA)
			}
			// And both must be valid k8s data keys (since neither input is empty).
			if !isValidDataKeyName(gotA) {
				t.Errorf("sanitizeUsernameForKey(%q) = %q is not a valid k8s data key", p.a, gotA)
			}
			if !isValidDataKeyName(gotB) {
				t.Errorf("sanitizeUsernameForKey(%q) = %q is not a valid k8s data key", p.b, gotB)
			}
		})
	}
}
