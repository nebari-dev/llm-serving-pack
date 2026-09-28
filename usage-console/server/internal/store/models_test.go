package store

import "testing"

func TestResolveAliasExactBeforePrefix(t *testing.T) {
	aliases := map[string]string{"summarize-*": "gpt-5-mini", "summarize-eu": "llama-3.3-70b"}
	for _, c := range []struct {
		requested, want string
		aliased         bool
	}{
		{"summarize-eu", "llama-3.3-70b", true},
		{"summarize-digest", "gpt-5-mini", true},
		{"summarize-", "gpt-5-mini", true},
		{"gpt-5.5", "gpt-5.5", false},
	} {
		got, ok := ResolveAlias(aliases, c.requested)
		if got != c.want || ok != c.aliased {
			t.Errorf("%s: got %s %v, want %s %v", c.requested, got, ok, c.want, c.aliased)
		}
	}
}
