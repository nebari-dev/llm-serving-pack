package routing

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
)

func readOwned(t *testing.T, path string) []Object {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Owned(b)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

// diffLines splits a change's diff into its added and removed lines.
func diffLines(c Change) (added, removed []string) {
	for _, l := range strings.Split(c.Diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			added = append(added, l[1:])
		case strings.HasPrefix(l, "-"):
			removed = append(removed, l[1:])
		}
	}
	return
}

// rulesOf is every rule of every AIGatewayRoute in objs, as YAML.
func rulesOf(t *testing.T, objs []Object) []string {
	t.Helper()
	var out []string
	for _, o := range objs {
		if o.Kind != "AIGatewayRoute" {
			continue
		}
		var spec struct {
			Rules []any `yaml:"rules"`
		}
		if err := get(o.node, "spec").Decode(&spec); err != nil {
			t.Fatal(err)
		}
		for _, r := range spec.Rules {
			out = append(out, encode(toNode(r)))
		}
	}
	return out
}

// The seed is the hand-written config as it stood when the console took it
// over, so compiling it gives the gateway the same rules: the hand-written
// config's, plus Warden's reroute hint for local and openrouter, which it
// lacked (a rule rerouting there fell through to the model's own route). The
// hints now live in their own AIGatewayRoute. Anthropic-style callers' copies
// of each rule (TestAnthropicCallersGetTheirOwnRules), which OpenAI-style
// callers never match, are left out.
func TestSeedCompilesToTodaysConfig(t *testing.T) {
	was := rulesOf(t, readOwned(t, "testdata/config-2026-10-05.yaml"))
	var now []string
	for _, r := range rulesOf(t, Compile(demo.Backends, demo.Routes)) {
		if !strings.Contains(r, "name: "+APIHeader) {
			now = append(now, r)
		}
	}
	extra := slices.Clone(now)
	for _, r := range was {
		i := slices.Index(extra, r)
		if i < 0 {
			t.Fatalf("compiled routing lacks the hand-written rule:\n%s", r)
		}
		extra = slices.Delete(extra, i, i+1)
	}
	if len(extra) != 2 || !strings.Contains(extra[0], "value: local") || !strings.Contains(extra[1], "value: openrouter") {
		t.Fatalf("compiled routing adds %d rules, want the local and openrouter hints:\n%s", len(extra), strings.Join(extra, "---\n"))
	}
}

// Gateway API allows 16 rules per route (an HTTPRoute, which each
// AIGatewayRoute becomes, with a not-found rule aigw adds; MaxRules is 14 to
// keep a rule and its Anthropic-style copy together): hints and routes each
// split across as many AIGatewayRoutes as they need, in order, so rule order
// still holds among equals (routes with the same match count are tried by
// name).
func TestCompileSplitsAt14Rules(t *testing.T) {
	var bs []model.Backend
	for i := range 20 {
		b := demo.Backends[0]
		b.Name = fmt.Sprintf("b%02d", i)
		bs = append(bs, b)
	}
	var rs []model.Route
	for i := range 33 {
		rs = append(rs, model.Route{Name: fmt.Sprintf("r%02d", i), Match: model.RouteMatch{Models: []string{fmt.Sprintf("m%02d", i)}}, Targets: []model.RouteTarget{{Backend: "b00"}}})
	}
	sizes := map[string]int{}
	var names []string
	for _, o := range Compile(bs, rs) {
		if o.Kind == "AIGatewayRoute" {
			names = append(names, o.Name)
			sizes[o.Name] = len(rulesOf(t, []Object{o}))
		}
	}
	if got, want := strings.Join(names, " "), "aigw-run aigw-run-2 aigw-run-3 stargate-hints stargate-hints-2"; got != want {
		t.Fatalf("AIGatewayRoutes = %s, want %s", got, want)
	}
	for n, want := range map[string]int{"aigw-run": 14, "aigw-run-2": 14, "aigw-run-3": 5, "stargate-hints": 14, "stargate-hints-2": 6} {
		if sizes[n] != want {
			t.Errorf("%s has %d rules, want %d", n, sizes[n], want)
		}
	}
	running := Compile(bs, rs)
	if !RouteInSync(running, rs[32], bs) {
		t.Errorf("a route in the third AIGatewayRoute isn't in sync with a config compiled from it")
	}
}

func TestOwnedLeavesInfrastructureOut(t *testing.T) {
	var names []string
	for _, o := range readOwned(t, "testdata/config-2026-10-05.yaml") {
		names = append(names, o.Kind+"/"+o.Name)
	}
	got := " " + strings.Join(names, " ")
	for _, infra := range []string{"Gateway/", "EnvoyProxy/", "SecurityPolicy/", "EnvoyExtensionPolicy/", "BackendTrafficPolicy/", "ClientTrafficPolicy/", "Backend/warden", "Backend/stargate-authz"} {
		if strings.Contains(got, " "+infra) {
			t.Errorf("owned objects include %s: %s", infra, got)
		}
	}
	for _, routing := range []string{"AIGatewayRoute/aigw-run", "Backend/openrouter", "AIServiceBackend/openrouter", "BackendSecurityPolicy/openrouter-key", "Secret/openrouter-key"} {
		if !strings.Contains(got, routing) {
			t.Errorf("owned objects lack %s: %s", routing, got)
		}
	}
}

func TestBackendWithoutEndpointIsNotCompiled(t *testing.T) {
	for _, o := range Compile(demo.Backends, demo.Routes) {
		if o.Name == "azure-openai-eu" {
			t.Fatalf("compiled %s/%s, a backend with no endpoint", o.Kind, o.Name)
		}
	}
}

func TestRuleYAML(t *testing.T) {
	r := model.Route{
		Name:  "research",
		Match: model.RouteMatch{Models: []string{"claude-*"}, Headers: []model.HeaderMatch{{Name: "x-stargate-team", Value: "research"}}},
		Targets: []model.RouteTarget{
			{Backend: "anthropic-prod", Weight: 3},
			{Backend: "bedrock-eu", Model: "claude-sonnet-5", Weight: 1},
		},
		Fallback: []model.RouteTarget{{Backend: "openai-prod", Model: "gpt-5.5"}},
	}
	want := `matches:
  - headers:
      - type: RegularExpression
        name: x-ai-eg-model
        value: claude-.*
      - name: x-stargate-team
        value: research
backendRefs:
  - name: anthropic-prod
    weight: 3
    priority: 0
  - name: bedrock-eu
    modelNameOverride: claude-sonnet-5
    weight: 1
    priority: 0
  - name: openai-prod
    modelNameOverride: gpt-5.5
    priority: 1
`
	if got := RuleYAML(r, nil); got != want {
		t.Errorf("RuleYAML =\n%s\nwant\n%s", got, want)
	}
}

func TestModelPatterns(t *testing.T) {
	for _, tc := range []struct{ models, want string }{
		{"gpt-5-mini", "- name: x-ai-eg-model\n        value: gpt-5-mini"},
		{"*", "- type: RegularExpression\n        name: x-ai-eg-model\n        value: .+"},
		{"gpt-5.*", "value: gpt-5\\..*"},
	} {
		r := model.Route{Name: "r", Match: model.RouteMatch{Models: []string{tc.models}}, Targets: []model.RouteTarget{{Backend: "openai-prod"}}}
		if got := RuleYAML(r, demo.Backends); !strings.Contains(got, tc.want) {
			t.Errorf("%s: RuleYAML =\n%s\nwant it to contain\n%s", tc.models, got, tc.want)
		}
	}
}

func TestDiff(t *testing.T) {
	one := Compile(demo.Backends[:1], nil) // openai-prod's Backend + AIServiceBackend, and the route
	two := Compile(demo.Backends[:2], nil) // plus anthropic-prod's
	kinds := func(cs []Change) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Change+" "+c.Kind+"/"+c.Name)
		}
		return strings.Join(s, ", ")
	}
	// anthropic-prod is Anthropic: its native twin, and hints for Anthropic-style callers, come with it.
	if got, want := kinds(Diff(one, two)), "changed AIGatewayRoute/stargate-hints, added Backend/anthropic-prod, added AIServiceBackend/anthropic-prod, added AIServiceBackend/anthropic-prod-native"; got != want {
		t.Errorf("Diff(one, two) = %s, want %s", got, want)
	}
	if got, want := kinds(Diff(two, one)), "changed AIGatewayRoute/stargate-hints, removed Backend/anthropic-prod, removed AIServiceBackend/anthropic-prod, removed AIServiceBackend/anthropic-prod-native"; got != want {
		t.Errorf("Diff(two, one) = %s, want %s", got, want)
	}
	if got := Diff(two, two); len(got) != 0 {
		t.Errorf("Diff(two, two) = %s, want none", kinds(got))
	}
	for _, c := range Diff(nil, two) {
		if added, removed := diffLines(c); len(added) == 0 || len(removed) != 0 {
			t.Errorf("an added %s's diff should be all additions:\n%s", c.Kind, c.Diff)
		}
	}
}

func TestInSync(t *testing.T) {
	running := Compile(demo.Backends, demo.Routes)
	sonnet := demo.Routes[2]
	if !RouteInSync(running, sonnet, demo.Backends) {
		t.Fatalf("%s isn't in sync with a config compiled from it", sonnet.Name)
	}
	moved := sonnet
	moved.Fallback = []model.RouteTarget{{Backend: "openai-prod"}}
	if RouteInSync(running, moved, demo.Backends) {
		t.Errorf("a changed fallback is in sync")
	}
	if !BackendInSync(running, demo.Backends[0]) {
		t.Errorf("%s isn't in sync with a config compiled from it", demo.Backends[0].Name)
	}
	b := demo.Backends[0]
	b.Endpoint = &model.BackendEndpoint{Schema: "OpenAI", Prefix: "/v2", Host: "example.com", Port: "443"}
	if BackendInSync(running, b) {
		t.Errorf("a changed endpoint is in sync")
	}
}

func TestRenderRoundTrips(t *testing.T) {
	base := []byte("apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata:\n  name: aigw-run\n")
	desired := Compile(demo.Backends, demo.Routes)
	out := Render(base, desired)
	if !strings.HasPrefix(string(out), string(base)) {
		t.Errorf("Render doesn't start with the base")
	}
	got, err := Owned(out)
	if err != nil {
		t.Fatal(err)
	}
	if cs := Diff(got, desired); len(cs) != 0 {
		t.Errorf("rendered config parses back with changes: %+v", cs)
	}
	// Placeholders stay plain so aigw substitutes them, and ports stay numbers.
	for _, want := range []string{"hostname: ${STARGATE_HOST:-localhost}", "port: ${LOCAL_LLM_PORT:-12434}", "port: 18090", "apiKey: ${OPENROUTER_API_KEY:-not-set}"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rendered config lacks %q", want)
		}
	}
}

func TestValidateRoute(t *testing.T) {
	ok := model.Route{Name: "research", Match: model.RouteMatch{Models: []string{"claude-opus-4-1"}, Headers: []model.HeaderMatch{{Name: "x-stargate-team", Value: "research"}}},
		Targets: []model.RouteTarget{{Backend: "anthropic-prod"}}, Fallback: []model.RouteTarget{{Backend: "openai-prod", Model: "gpt-5.5"}}}
	for _, tc := range []struct {
		name string
		edit func(*model.Route)
		want string
	}{
		{"valid", func(*model.Route) {}, ""},
		{"name", func(r *model.Route) { r.Name = "Research Team" }, "name must be lowercase letters, digits, dots and dashes"},
		{"no models", func(r *model.Route) { r.Match.Models = nil }, "match at least one model"},
		{"pattern mixed with names", func(r *model.Route) { r.Match.Models = []string{"gpt-5-mini", "claude-*"} }, `"claude-*" must be the route's only model`},
		{"star in the middle", func(r *model.Route) { r.Match.Models = []string{"claude-*-5"} }, `"claude-*-5": * only goes at the end`},
		{"blank model", func(r *model.Route) { r.Match.Models = []string{" "} }, "a model name can't be blank"},
		{"reserved header", func(r *model.Route) { r.Match.Headers = []model.HeaderMatch{{Name: "x-ai-eg-model", Value: "x"}} }, "x-ai-eg-model is set by the gateway; match models instead"},
		{"warden header", func(r *model.Route) { r.Match.Headers = []model.HeaderMatch{{Name: "x-stargate-backend", Value: "x"}} }, "x-stargate-backend is Warden's reroute hint"},
		{"header name", func(r *model.Route) { r.Match.Headers = []model.HeaderMatch{{Name: "X Team", Value: "x"}} }, `header "X Team": lowercase letters, digits and dashes`},
		{"header value", func(r *model.Route) { r.Match.Headers = []model.HeaderMatch{{Name: "x-team", Value: ""}} }, "header x-team needs a value"},
		{"duplicate header", func(r *model.Route) {
			r.Match.Headers = []model.HeaderMatch{{Name: "x-team", Value: "a"}, {Name: "x-team", Value: "b"}}
		}, "header x-team appears twice"},
		{"no targets", func(r *model.Route) { r.Targets = nil }, "send to at least one backend"},
		{"unknown backend", func(r *model.Route) { r.Targets = []model.RouteTarget{{Backend: "mars"}} }, `no backend "mars"`},
		{"no endpoint", func(r *model.Route) { r.Targets = []model.RouteTarget{{Backend: "azure-openai-eu"}} }, "azure-openai-eu has no endpoint, so the gateway can't reach it"},
		{"weights", func(r *model.Route) {
			r.Targets = []model.RouteTarget{{Backend: "anthropic-prod", Weight: 1}, {Backend: "bedrock-eu"}}
		}, "every target needs a weight of at least 1 when there's more than one"},
		{"target twice", func(r *model.Route) {
			r.Targets = []model.RouteTarget{{Backend: "anthropic-prod", Weight: 1}, {Backend: "anthropic-prod", Weight: 1}}
		}, "anthropic-prod is a target twice"},
		{"fallback is a target", func(r *model.Route) { r.Fallback = []model.RouteTarget{{Backend: "anthropic-prod"}} }, "anthropic-prod is already a target"},
		{"fallback twice", func(r *model.Route) {
			r.Fallback = []model.RouteTarget{{Backend: "openai-prod"}, {Backend: "openai-prod"}}
		}, "openai-prod is in the fallback twice"},
		{"fallback weight", func(r *model.Route) { r.Fallback = []model.RouteTarget{{Backend: "openai-prod", Weight: 2}} }, "fallback backends are tried in order; they take no weight"},
		{"same model, same headers", func(r *model.Route) { r.Match.Headers = nil }, `claude-opus-4-1 is already routed by "claude-opus-4-1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ok
			r.Match.Headers = append([]model.HeaderMatch(nil), ok.Match.Headers...)
			tc.edit(&r)
			err := ValidateRoute(r, demo.Routes, demo.Backends)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case tc.want != "" && (err == nil || err.Error() != tc.want):
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A new route goes ahead of any route that takes every model, so the
// catch-all keeps catching only what nothing else does.
func TestInsertAt(t *testing.T) {
	all := model.Route{Name: "default", Match: model.RouteMatch{Models: []string{"*"}}}
	rs := append(append([]model.Route(nil), demo.Routes...), all)
	if got := InsertAt(rs, model.Route{Match: model.RouteMatch{Models: []string{"x"}}}); got != len(demo.Routes) {
		t.Errorf("InsertAt = %d, want %d (before the catch-all)", got, len(demo.Routes))
	}
	if got := InsertAt(demo.Routes, model.Route{Match: model.RouteMatch{Models: []string{"x"}}}); got != len(demo.Routes) {
		t.Errorf("InsertAt with no catch-all = %d, want the end", got)
	}
}

func TestBackendYAML(t *testing.T) {
	got := BackendYAML(demo.Backends[5]) // openrouter
	for _, want := range []string{"kind: Backend\n", "kind: AIServiceBackend\n", "kind: BackendSecurityPolicy\n", "kind: Secret\n", "sni: openrouter.ai"} {
		if !strings.Contains(got, want) {
			t.Errorf("BackendYAML(openrouter) lacks %q", want)
		}
	}
	if got := BackendYAML(demo.Backends[6]); got != "" {
		t.Errorf("a backend with no endpoint compiles to %q", got)
	}
}
