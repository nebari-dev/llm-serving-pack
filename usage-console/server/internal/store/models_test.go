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

func TestOverlappingPatternsLongestPrefixWins(t *testing.T) {
	aliases := map[string]string{"gpt-*": "gpt-5.5", "gpt-5-*": "gpt-5-mini", "gpt-5-m*": "claude-haiku-4-5"}
	for i := 0; i < 50; i++ { // map order varies run to run; the answer mustn't
		for requested, want := range map[string]string{"gpt-5-mx": "claude-haiku-4-5", "gpt-5-nano": "gpt-5-mini", "gpt-4o": "gpt-5.5"} {
			if got, _ := ResolveAlias(aliases, requested); got != want {
				t.Fatalf("%s: got %s, want %s", requested, got, want)
			}
		}
	}
}

func TestValidateAlias(t *testing.T) {
	catalog := []string{"gpt-5-mini", "gpt-5.5", "claude-sonnet-5"}
	for _, c := range []struct {
		alias, target, err string
	}{
		{"summarize-*", "gpt-5-mini", ""},
		{"fast", "gpt-5-mini", ""},
		{"gpt-4o", "gpt-5.5", ""}, // retiring a model name is what aliases are for
		{"", "gpt-5-mini", "alias is required"},
		{"has space", "gpt-5-mini", "alias can't contain spaces"},
		{"*", "gpt-5-mini", "a pattern needs a prefix before the *"},
		{"a*b", "gpt-5-mini", "* may only end an alias"},
		{"a**", "gpt-5-mini", "* may only end an alias"},
		{"fast", "gpt-9", `unknown target model "gpt-9"`},
		{"gpt-5-mini", "gpt-5-mini", "an alias can't point at itself"},
		// A pattern that would capture real catalog models reroutes their traffic.
		{"gpt-*", "gpt-5-mini", "gpt-* would also capture gpt-5.5, which the catalog serves directly"},
	} {
		got := ""
		if err := ValidateAlias(c.alias, c.target, catalog); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%q → %q: got %q, want %q", c.alias, c.target, got, c.err)
		}
	}
}
