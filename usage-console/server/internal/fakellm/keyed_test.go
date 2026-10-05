package fakellm

import "testing"

// The keyed backend stands in for a provider that needs its own key: only
// KeyedKey gets through. Every other backend takes any provider key, as before.
func TestKeyedBackendRequiresItsKey(t *testing.T) {
	for _, tc := range []struct {
		backend, auth string
		want          bool
	}{
		{KeyedBackend, "Bearer " + KeyedKey, true},
		{KeyedBackend, "Bearer sk-wrong", false},
		{KeyedBackend, "", false},
		{KeyedBackend, KeyedKey, false},
		{"openai-prod", "", true},
		{"openai-prod", "Bearer anything", true},
	} {
		if got := Authorized(tc.backend, tc.auth); got != tc.want {
			t.Errorf("Authorized(%q, %q) = %v, want %v", tc.backend, tc.auth, got, tc.want)
		}
	}
	if ms := ModelsFor(KeyedBackend); len(ms) == 0 || ms[0] != KeyedModel {
		t.Errorf("ModelsFor(keyed) = %v", ms)
	}
	if ms := ModelsFor("openai-prod"); len(ms) == 0 {
		t.Errorf("ModelsFor(openai-prod) is empty")
	}
}
