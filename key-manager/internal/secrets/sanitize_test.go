package secrets

import "testing"

func TestSanitizeUsernameForKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
	}{
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
			name:  "single @ becomes -at-",
			input: "alice@example.com",
			want:  "alice-at-example.com",
		},
		{
			name:  "multiple @ each become -at-",
			input: "weird@@example.com",
			want:  "weird-at--at-example.com",
		},
		{
			name:  "plus is replaced with dash",
			input: "alice+tag@example.com",
			want:  "alice-tag-at-example.com",
		},
		{
			name:  "space is replaced with dash",
			input: "alice smith",
			want:  "alice-smith",
		},
		{
			name:  "unicode bytes are each replaced with dash",
			input: "alicé",
			want:  "alic--", // é is two bytes in UTF-8, both invalid
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
			// And the result must itself be a valid k8s data key name.
			if !isValidDataKeyName(got) {
				t.Errorf("sanitizeUsernameForKey(%q) = %q is not a valid k8s data key", tc.input, got)
			}
		})
	}
}
