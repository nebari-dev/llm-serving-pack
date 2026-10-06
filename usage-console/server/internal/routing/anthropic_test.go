package routing

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
	"gopkg.in/yaml.v3"
)

func claudeBackend() model.Backend {
	return model.Backend{Name: "claude", Provider: "Anthropic", Region: "us-east", Models: []string{"claude-sonnet-5"},
		Endpoint: &model.BackendEndpoint{Schema: "OpenAI", Prefix: "/v1", Host: "api.anthropic.com", Port: "443", TLS: true, APIKeyEnv: "STARGATE_PROVIDER_KEY_CLAUDE"}}
}

func object(t *testing.T, objs []Object, kind, name string) map[string]any {
	t.Helper()
	o, ok := find(objs, kind, name)
	if !ok {
		var have []string
		for _, x := range objs {
			have = append(have, x.key())
		}
		t.Fatalf("no %s/%s in %s", kind, name, strings.Join(have, " "))
	}
	v, _ := o.value().(map[string]any)
	return v
}

func specOf(t *testing.T, objs []Object, kind, name string) string {
	t.Helper()
	b, _ := yaml.Marshal(object(t, objs, kind, name)["spec"])
	return string(b)
}

// An Anthropic provider serves both kinds of caller from one backend: its
// OpenAI-compatible endpoint for OpenAI-style callers (as before), and a
// second AIServiceBackend, <name>-native, on Anthropic's own Messages API for
// Anthropic-style callers. Both reach the same host with the same key: the
// native one sends it as x-api-key (AnthropicAPIKey), from the same Secret.
func TestAnthropicBackendCompilesANativeTwin(t *testing.T) {
	objs := backendObjects(claudeBackend())
	var names []string
	for _, o := range objs {
		names = append(names, o.key())
	}
	want := []string{"Backend/claude", "AIServiceBackend/claude", "BackendSecurityPolicy/claude-key", "Secret/claude-key",
		"AIServiceBackend/claude-native", "BackendSecurityPolicy/claude-native-key"}
	if !slices.Equal(names, want) {
		t.Fatalf("objects = %v, want %v", names, want)
	}
	if got := specOf(t, objs, "AIServiceBackend", "claude"); !strings.Contains(got, "name: OpenAI") || !strings.Contains(got, "prefix: /v1") {
		t.Errorf("the OpenAI-compatible twin changed:\n%s", got)
	}
	native := specOf(t, objs, "AIServiceBackend", "claude-native")
	for _, s := range []string{"name: Anthropic", "prefix: /v1", "name: claude\n"} {
		if !strings.Contains(native, s) {
			t.Errorf("native AIServiceBackend lacks %q:\n%s", s, native)
		}
	}
	bsp := specOf(t, objs, "BackendSecurityPolicy", "claude-native-key")
	for _, s := range []string{"type: AnthropicAPIKey", "anthropicAPIKey:", "name: claude-key", "name: claude-native"} {
		if !strings.Contains(bsp, s) {
			t.Errorf("native BackendSecurityPolicy lacks %q:\n%s", s, bsp)
		}
	}
	if strings.Contains(bsp, "apiKey:\n") {
		t.Errorf("an AnthropicAPIKey policy takes only anthropicAPIKey:\n%s", bsp)
	}

	// No key (a self-hosted Anthropic-compatible server): no policy either.
	b := claudeBackend()
	b.Endpoint.APIKeyEnv = ""
	for _, o := range backendObjects(b) {
		if o.Kind == "BackendSecurityPolicy" || o.Kind == "Secret" {
			t.Errorf("a keyless backend compiles %s", o.key())
		}
	}
	// Any other provider compiles as before.
	for _, o := range backendObjects(demoBackend("openai-prod")) {
		if strings.HasSuffix(o.Name, NativeSuffix) {
			t.Errorf("openai-prod compiles %s", o.key())
		}
	}
}

// matchCount is how many header matches each of a rule's matches has.
func matchCounts(r rule) []int {
	var out []int
	for _, m := range r.Matches {
		out = append(out, len(m.Headers))
	}
	return out
}

// Anthropic-style callers (POST /anthropic/v1/messages) carry the key
// check's x-stargate-api: anthropic. Every rule gets a copy for them, right
// after it in the same AIGatewayRoute, with that header added and each
// Anthropic backend swapped for its native twin; the other backends stay
// (Agent Router translates Messages to their OpenAI API). One more header
// than its original means the copy always wins for them, and the copies keep
// the originals' order among themselves; OpenAI-style callers never match
// one, so their routing is as before. A route and its copy are one change.
func TestAnthropicCallersGetTheirOwnRules(t *testing.T) {
	bs := append(slices.Clone(demo.Backends), claudeBackend())
	rs := append(slices.Clone(demo.Routes), model.Route{Name: "team-claude",
		Match:   model.RouteMatch{Models: []string{"claude-*"}, Headers: []model.HeaderMatch{{Name: "x-team", Value: "ml"}}},
		Targets: []model.RouteTarget{{Backend: "claude", Weight: 3}, {Backend: "openai-prod", Model: "gpt-5.5", Weight: 1}}})
	objs := Compile(bs, rs)

	routes, hints := 0, 0
	for _, o := range objs {
		if o.Kind != "AIGatewayRoute" {
			continue
		}
		var spec struct{ Rules []rule }
		if err := get(o.node, "spec").Decode(&spec); err != nil {
			t.Fatal(err)
		}
		if len(spec.Rules)%2 != 0 || len(spec.Rules) > MaxRules {
			t.Fatalf("%s has %d rules, want pairs, at most %d", o.Name, len(spec.Rules), MaxRules)
		}
		if strings.HasPrefix(o.Name, HintRouteName) {
			hints += len(spec.Rules) / 2
		} else {
			routes += len(spec.Rules) / 2
		}
		for i := 0; i < len(spec.Rules); i += 2 {
			o, a := spec.Rules[i], spec.Rules[i+1]
			for _, m := range o.Matches {
				if slices.ContainsFunc(m.Headers, func(h header) bool { return h.Name == APIHeader }) {
					t.Errorf("an OpenAI-style rule matches %s: %+v", APIHeader, o)
				}
			}
			oc, ac := matchCounts(o), matchCounts(a)
			for j := range oc {
				oc[j]++
			}
			if !slices.Equal(oc, ac) {
				t.Errorf("rule %d: Anthropic copy has %v header matches, want one more than %v", i, ac, matchCounts(o))
			}
			for _, m := range a.Matches {
				if last := m.Headers[len(m.Headers)-1]; last != (header{Name: APIHeader, Value: AnthropicAPI}) {
					t.Errorf("rule %d: Anthropic copy's last header = %+v", i, last)
				}
			}
			if len(a.BackendRefs) != len(o.BackendRefs) {
				t.Fatalf("rule %d: backendRefs %v vs %v", i, a.BackendRefs, o.BackendRefs)
			}
			for j, br := range o.BackendRefs {
				want := br
				if br.Name == "claude" || br.Name == "anthropic-prod" {
					want.Name += NativeSuffix
				}
				if got := a.BackendRefs[j]; got.Name != want.Name || got.ModelNameOverride != want.ModelNameOverride || !eqp(got.Weight, want.Weight) || !eqp(got.Priority, want.Priority) {
					t.Errorf("rule %d ref %d = %+v, want %+v", i, j, got, want)
				}
			}
		}
	}
	withEndpoint := 0
	for _, b := range bs {
		if b.Endpoint != nil {
			withEndpoint++
		}
	}
	if routes != len(rs) || hints != withEndpoint {
		t.Errorf("%d route pairs for %d routes, %d hint pairs for %d backends", routes, len(rs), hints, withEndpoint)
	}

	// The seeded claude-sonnet-5 route: native anthropic-prod first, bedrock-eu (OpenAI schema, translated) as fallback.
	if got := RuleYAML(demo.Routes[2], bs); !strings.Contains(got, "name: anthropic-prod-native") || !strings.Contains(got, "value: anthropic") {
		t.Errorf("RuleYAML doesn't show the Anthropic-style rule:\n%s", got)
	}
	// A route that sends to no Anthropic backend shows its one rule.
	if got := RuleYAML(demo.Routes[0], bs); strings.Contains(got, APIHeader) {
		t.Errorf("RuleYAML(gpt-5) shows an Anthropic-style copy that changes nothing:\n%s", got)
	}

	// In sync only once the gateway runs both: here it runs claude as an
	// OpenAI-compatible backend, so the copy still sends to claude.
	if !RouteInSync(objs, rs[len(rs)-1], bs) {
		t.Errorf("team-claude isn't in sync with a config compiled from it")
	}
	was := slices.Clone(bs)
	was[len(was)-1].Provider = "OpenAI-compatible"
	if RouteInSync(Compile(was, rs), rs[len(rs)-1], bs) {
		t.Errorf("team-claude is in sync with a gateway whose Anthropic-style rule sends to claude")
	}
}

// Pairs split at 7 routes, so a rule and its copy are always in the same
// AIGatewayRoute.
func TestAnthropicPairsSplitAt7Routes(t *testing.T) {
	var rs []model.Route
	for i := range 20 {
		rs = append(rs, model.Route{Name: fmt.Sprintf("r%02d", i), Match: model.RouteMatch{Models: []string{fmt.Sprintf("m%02d", i)}}, Targets: []model.RouteTarget{{Backend: "claude"}}})
	}
	sizes := map[string]int{}
	for _, o := range Compile([]model.Backend{claudeBackend()}, rs) {
		if o.Kind == "AIGatewayRoute" {
			sizes[o.Name] = len(rulesOf(t, []Object{o}))
		}
	}
	if want := map[string]int{"aigw-run": 14, "aigw-run-2": 14, "aigw-run-3": 12, "stargate-hints": 2}; !maps.Equal(sizes, want) {
		t.Errorf("rules per AIGatewayRoute = %v, want %v", sizes, want)
	}
}

func eqp(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// With no Anthropic backend, Anthropic-style callers use the same rules as
// everyone (Agent Router translates), so nothing is added.
func TestNoAnthropicBackendNoAnthropicRules(t *testing.T) {
	var bs []model.Backend
	for _, b := range demo.Backends {
		if b.Provider != "Anthropic" {
			bs = append(bs, b)
		}
	}
	for _, o := range Compile(bs, demo.Routes[:2]) {
		if strings.Contains(o.Name, "anthropic") || strings.HasSuffix(o.Name, NativeSuffix) {
			t.Errorf("compiled %s with no Anthropic backend", o.key())
		}
	}
}

// The native twin's Secret is the backend's own, so the config file's
// ownership sees it through either policy.
func TestOwnedReadsAnthropicKeyPolicies(t *testing.T) {
	b := claudeBackend()
	out := Render(nil, backendObjects(b))
	owned, err := Owned(out)
	if err != nil {
		t.Fatal(err)
	}
	if cs := Diff(owned, backendObjects(b)); len(cs) != 0 {
		t.Errorf("rendered Anthropic backend parses back with changes: %+v", cs)
	}
	if !BackendInSync(owned, b) {
		t.Errorf("BackendInSync is false for the config compiled from it")
	}
	// The native twin alone, without its Secret's other policy, still owns the Secret.
	var nativeOnly []byte
	for _, o := range backendObjects(b) {
		if o.Kind == "BackendSecurityPolicy" && o.Name == "claude-key" {
			continue
		}
		nativeOnly = append(nativeOnly, []byte("---\n"+o.YAML())...)
	}
	owned, _ = Owned(nativeOnly)
	if _, ok := find(owned, "Secret", "claude-key"); !ok {
		t.Errorf("Secret/claude-key isn't owned through the AnthropicAPIKey policy")
	}
}

func TestValidateAnthropicBackendNames(t *testing.T) {
	b := claudeBackend()
	if err := ValidateBackend(b, nil); err != nil {
		t.Fatalf("claude: %v", err)
	}
	// <name>-native-key is the native policy's name, so the name is 52 at most.
	b.Name = strings.Repeat("a", 53)
	if err := ValidateBackend(b, nil); err == nil || !strings.Contains(err.Error(), "52") {
		t.Errorf("a 53-character Anthropic backend: %v", err)
	}
	b.Name = strings.Repeat("a", 52)
	if err := ValidateBackend(b, nil); err != nil {
		t.Errorf("a 52-character Anthropic backend: %v", err)
	}
	// -native names an Anthropic backend's twin; no backend may take it.
	o := newBackend()
	o.Name = "claude-native"
	if err := ValidateBackend(o, nil); err == nil || !strings.Contains(err.Error(), "-native") {
		t.Errorf("a backend named claude-native: %v", err)
	}
}
