package secrets

import "testing"

// Each row pins a literal expected output. The hash suffixes are a
// 128-bit-truncated SHA-256 over the raw username; pinning them as literals
// (rather than computing them via sha256 in the test) means any change to
// either the substitution rules OR the hash algorithm fails the test loudly.
// That is the drift detector. The companion black-box test in manager_test.go
// has its own independent reproduction of the rule used to assemble end-to-end
// expected clientIDs.
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
			want:  "alice-at-example.com-ff8d9819fc0e12bf0d24892e45987e24",
		},
		{
			name:  "multiple @ each become -at- with hash suffix",
			input: "weird@@example.com",
			want:  "weird-at--at-example.com-d5641a0e3fa38a581fbd6ffd29f970cc",
		},
		{
			name:  "plus is replaced with dash",
			input: "alice+tag@example.com",
			want:  "alice-tag-at-example.com-567e25214281f7004cf65c7a16fd5868",
		},
		{
			name:  "space is replaced with dash",
			input: "alice smith",
			want:  "alice-smith-038bd15a1ecf0f7b85db10beda1695ed",
		},
		{
			name:  "unicode bytes are each replaced with dash",
			input: "alicé",
			want:  "alic---6dba1f9539a0e88583e9034e656a3822", // é is two bytes in UTF-8, both invalid
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
			if !isValidDataKeyName(gotA) {
				t.Errorf("sanitizeUsernameForKey(%q) = %q is not a valid k8s data key", p.a, gotA)
			}
			if !isValidDataKeyName(gotB) {
				t.Errorf("sanitizeUsernameForKey(%q) = %q is not a valid k8s data key", p.b, gotB)
			}
		})
	}
}
