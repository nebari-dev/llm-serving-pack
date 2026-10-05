// Package routing compiles the console's desired routing state (routes and
// backends in Postgres, spec §4.4) to the gateway's resources (AIGatewayRoute,
// Backend, AIServiceBackend and a provider key's BackendSecurityPolicy and
// Secret), and diffs that against what the gateway is running. An Applier
// puts it in front of the gateway.
package routing

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jbouder/stargate/server/internal/model"
	"gopkg.in/yaml.v3"
)

// Namespace is where aigw's standalone config keeps everything.
const Namespace = "default"

// RouteName is the one AIGatewayRoute the console's routes compile into. The
// gateway's BackendTrafficPolicy (retries, health checks) targets the HTTPRoute
// generated from it by this name.
const RouteName = "aigw-run"

// Object is one resource of the gateway's config, comments stripped.
type Object struct {
	Kind, Name string
	node       *yaml.Node
}

// YAML is the object as it would be written to a config file.
func (o Object) YAML() string { return encode(o.node) }

func (o Object) key() string { return o.Kind + "/" + o.Name }

// value is the object decoded, for comparing: key order doesn't matter, list
// order does.
func (o Object) value() any {
	var v any
	_ = o.node.Decode(&v)
	return v
}

func encode(n *yaml.Node) string {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	_ = e.Encode(n)
	_ = e.Close()
	return b.String()
}

func toNode(v any) *yaml.Node {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		panic(err) // only our own types are encoded
	}
	return &n
}

func newObject(apiVersion, kind, name string, spec any) Object {
	return Object{Kind: kind, Name: name, node: toNode(doc{APIVersion: apiVersion, Kind: kind, Metadata: meta{Name: name, Namespace: Namespace}, Spec: spec})}
}

type doc struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   meta              `yaml:"metadata"`
	Type       string            `yaml:"type,omitempty"`
	StringData map[string]string `yaml:"stringData,omitempty"`
	Spec       any               `yaml:"spec,omitempty"`
}

type meta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type ref struct {
	Name  string `yaml:"name,omitempty"`
	Kind  string `yaml:"kind,omitempty"`
	Group string `yaml:"group,omitempty"`
}

type groupRef struct {
	Group string `yaml:"group"`
	Kind  string `yaml:"kind"`
	Name  string `yaml:"name"`
}

type routeSpec struct {
	ParentRefs      []ref  `yaml:"parentRefs"`
	Rules           []rule `yaml:"rules"`
	LLMRequestCosts []cost `yaml:"llmRequestCosts"`
}

type rule struct {
	Matches     []match      `yaml:"matches"`
	BackendRefs []backendRef `yaml:"backendRefs"`
}

type match struct {
	Headers []header `yaml:"headers"`
}

type header struct {
	Type  string `yaml:"type,omitempty"`
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type backendRef struct {
	Name              string `yaml:"name"`
	ModelNameOverride string `yaml:"modelNameOverride,omitempty"`
	Weight            *int   `yaml:"weight,omitempty"`
	Priority          *int   `yaml:"priority,omitempty"`
}

type cost struct {
	MetadataKey string `yaml:"metadataKey"`
	Type        string `yaml:"type"`
}

// Token costs Agent Router writes to dynamic metadata, which the access log
// (and so every receipt) reads.
var costs = []cost{
	{"llm_input_token", "InputToken"},
	{"llm_cached_input_token", "CachedInputToken"},
	{"llm_output_token", "OutputToken"},
	{"llm_reasoning_token", "ReasoningToken"},
	{"llm_cache_creation_input_token", "CacheCreationInputToken"},
}

type backendSpec struct {
	Endpoints []endpoint `yaml:"endpoints"`
	TLS       *tlsSpec   `yaml:"tls,omitempty"`
}

type endpoint struct {
	FQDN fqdn `yaml:"fqdn"`
}

type fqdn struct {
	Hostname string `yaml:"hostname"`
	Port     port   `yaml:"port"`
}

// port is a number, or an aigw ${VAR:-default} that becomes one.
type port string

func (p port) MarshalYAML() (any, error) {
	if n, err := strconv.Atoi(string(p)); err == nil {
		return n, nil
	}
	return string(p), nil
}

type tlsSpec struct {
	SNI                     string `yaml:"sni"`
	WellKnownCACertificates string `yaml:"wellKnownCACertificates"`
}

type serviceBackendSpec struct {
	Schema         schema         `yaml:"schema"`
	HeaderMutation headerMutation `yaml:"headerMutation"`
	BackendRef     ref            `yaml:"backendRef"`
}

type schema struct {
	Name   string `yaml:"name"`
	Prefix string `yaml:"prefix,omitempty"`
}

type headerMutation struct {
	Remove []string `yaml:"remove"`
}

type securityPolicySpec struct {
	TargetRefs []groupRef `yaml:"targetRefs"`
	Type       string     `yaml:"type"`
	APIKey     apiKey     `yaml:"apiKey"`
}

type apiKey struct {
	SecretRef meta `yaml:"secretRef"`
}

// HintHeader is Warden's reroute hint: the backend a rule chose. Each backend
// gets a rule matching it plus any model, and two header matches outrank a
// route's one.
const HintHeader = "x-stargate-backend"

// ModelHeader is where Agent Router puts the request's model.
const ModelHeader = "x-ai-eg-model"

func intp(i int) *int { return &i }

// Compile is the gateway config for the desired state: the AIGatewayRoute,
// then each backend's resources. A backend with no endpoint is left out.
func Compile(backends []model.Backend, routes []model.Route) []Object {
	spec := routeSpec{ParentRefs: []ref{{Name: RouteName, Kind: "Gateway", Group: "gateway.networking.k8s.io"}}, LLMRequestCosts: costs}
	for _, b := range backends {
		if b.Endpoint != nil {
			spec.Rules = append(spec.Rules, rule{
				Matches:     []match{{Headers: []header{{Name: HintHeader, Value: b.Name}, {Type: "RegularExpression", Name: ModelHeader, Value: ".+"}}}},
				BackendRefs: []backendRef{{Name: b.Name}},
			})
		}
	}
	for _, r := range routes {
		spec.Rules = append(spec.Rules, compileRule(r))
	}
	out := []Object{newObject("aigateway.envoyproxy.io/v1beta1", "AIGatewayRoute", RouteName, spec)}
	for _, b := range backends {
		out = append(out, backendObjects(b)...)
	}
	return out
}

func backendObjects(b model.Backend) []Object {
	e := b.Endpoint
	if e == nil {
		return nil
	}
	bs := backendSpec{Endpoints: []endpoint{{FQDN: fqdn{Hostname: e.Host, Port: port(e.Port)}}}}
	if e.TLS {
		bs.TLS = &tlsSpec{SNI: e.Host, WellKnownCACertificates: "System"}
	}
	out := []Object{
		newObject("gateway.envoyproxy.io/v1alpha1", "Backend", b.Name, bs),
		newObject("aigateway.envoyproxy.io/v1beta1", "AIServiceBackend", b.Name, serviceBackendSpec{
			Schema:         schema{Name: e.Schema, Prefix: e.Prefix},
			HeaderMutation: headerMutation{Remove: []string{HintHeader}},
			BackendRef:     ref{Name: b.Name, Kind: "Backend", Group: "gateway.envoyproxy.io"},
		}),
	}
	if e.APIKeyEnv != "" {
		secret := b.Name + "-key"
		out = append(out,
			newObject("aigateway.envoyproxy.io/v1beta1", "BackendSecurityPolicy", secret, securityPolicySpec{
				TargetRefs: []groupRef{{Group: "aigateway.envoyproxy.io", Kind: "AIServiceBackend", Name: b.Name}},
				Type:       "APIKey",
				APIKey:     apiKey{SecretRef: meta{Name: secret, Namespace: Namespace}},
			}),
			Object{Kind: "Secret", Name: secret, node: toNode(doc{
				APIVersion: "v1", Kind: "Secret", Metadata: meta{Name: secret, Namespace: Namespace}, Type: "Opaque",
				StringData: map[string]string{"apiKey": "${" + e.APIKeyEnv + ":-not-set}"},
			})},
		)
	}
	return out
}

// BackendYAML is the resources a backend compiles to, or "" for one with no
// endpoint.
func BackendYAML(b model.Backend) string {
	var out []string
	for _, o := range backendObjects(b) {
		out = append(out, o.YAML())
	}
	return strings.Join(out, "---\n")
}

// modelHeader is the header match for one of a route's models.
func modelHeader(m string) header {
	switch {
	case m == "*":
		return header{Type: "RegularExpression", Name: ModelHeader, Value: ".+"}
	case strings.HasSuffix(m, "*"):
		return header{Type: "RegularExpression", Name: ModelHeader, Value: regexp.QuoteMeta(strings.TrimSuffix(m, "*")) + ".*"}
	}
	return header{Name: ModelHeader, Value: m}
}

func compileRule(r model.Route) rule {
	var out rule
	for _, m := range r.Match.Models {
		hs := []header{modelHeader(m)}
		for _, h := range r.Match.Headers {
			hs = append(hs, header{Name: h.Name, Value: h.Value})
		}
		out.Matches = append(out.Matches, match{Headers: hs})
	}
	for _, t := range r.Targets {
		br := backendRef{Name: t.Backend, ModelNameOverride: t.Model}
		if len(r.Targets) > 1 {
			br.Weight = intp(t.Weight)
		}
		if len(r.Fallback) > 0 {
			br.Priority = intp(0)
		}
		out.BackendRefs = append(out.BackendRefs, br)
	}
	for i, t := range r.Fallback {
		out.BackendRefs = append(out.BackendRefs, backendRef{Name: t.Backend, ModelNameOverride: t.Model, Priority: intp(i + 1)})
	}
	return out
}

// RuleYAML is the AIGatewayRoute rule a route compiles to.
func RuleYAML(r model.Route) string { return encode(toNode(compileRule(r))) }

// Owned is the routing in a gateway config file: every AIGatewayRoute,
// AIServiceBackend and BackendSecurityPolicy, and the Backends and Secrets
// they name. The rest (the Gateway, its policies, Warden's and the key
// check's Backends) is infrastructure the console doesn't write.
func Owned(file []byte) ([]Object, error) {
	var all []Object
	d := yaml.NewDecoder(bytes.NewReader(file))
	for {
		var n yaml.Node
		err := d.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(n.Content) == 0 || n.Content[0].Kind != yaml.MappingNode {
			continue
		}
		body := n.Content[0]
		stripComments(body)
		all = append(all, Object{Kind: scalar(body, "kind"), Name: scalar(get(body, "metadata"), "name"), node: body})
	}
	backends, secrets := map[string]bool{}, map[string]bool{}
	for _, o := range all {
		spec := get(o.node, "spec")
		switch o.Kind {
		case "AIServiceBackend":
			backends[scalar(get(spec, "backendRef"), "name")] = true
		case "BackendSecurityPolicy":
			secrets[scalar(get(get(spec, "apiKey"), "secretRef"), "name")] = true
		}
	}
	var out []Object
	for _, o := range all {
		switch {
		case o.Kind == "AIGatewayRoute", o.Kind == "AIServiceBackend", o.Kind == "BackendSecurityPolicy",
			o.Kind == "Backend" && backends[o.Name], o.Kind == "Secret" && secrets[o.Name]:
			out = append(out, o)
		}
	}
	return out, nil
}

func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(m *yaml.Node, key string) string {
	if n := get(m, key); n != nil && n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return ""
}

func stripComments(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		stripComments(c)
	}
}

// generatedHeader starts the console's part of the config file.
const generatedHeader = `# Routing, generated by stargate-api from the console's desired state in
# Postgres. Edit routes in the console (or its API) and apply; edits made here
# are replaced on the next apply. Infrastructure is in aigw/base.yaml.
`

// Render is a whole gateway config: the infrastructure base, then the routing.
func Render(base []byte, objs []Object) []byte {
	var b bytes.Buffer
	b.Write(base)
	if len(base) > 0 && !bytes.HasSuffix(base, []byte("\n")) {
		b.WriteByte('\n')
	}
	for i, o := range objs {
		b.WriteString("---\n")
		if i == 0 {
			b.WriteString(generatedHeader)
		}
		b.WriteString(o.YAML())
	}
	return b.Bytes()
}

// Change is one resource the gateway would add, change or remove on apply.
// Diff is the resource's YAML with "+", "-" or " " before each line.
type Change struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Change string `json:"change"` // added, changed or removed
	Diff   string `json:"diff"`
}

// Diff is what applying desired would change in running: desired's order,
// then what goes away in running's.
func Diff(running, desired []Object) []Change {
	have := map[string]Object{}
	for _, o := range running {
		have[o.key()] = o
	}
	want := map[string]bool{}
	var out []Change
	for _, o := range desired {
		want[o.key()] = true
		r, ok := have[o.key()]
		switch {
		case !ok:
			out = append(out, Change{o.Kind, o.Name, "added", lineDiff("", o.YAML())})
		case !reflect.DeepEqual(r.value(), o.value()):
			// Lay the running copy's keys out like ours, so the diff shows
			// only what differs.
			shown := cloneNode(r.node)
			orderLike(shown, o.node)
			out = append(out, Change{o.Kind, o.Name, "changed", lineDiff(encode(shown), o.YAML())})
		}
	}
	for _, o := range running {
		if !want[o.key()] {
			out = append(out, Change{o.Kind, o.Name, "removed", lineDiff(o.YAML(), "")})
		}
	}
	return out
}

func cloneNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, x := range n.Content {
		c.Content[i] = cloneNode(x)
	}
	return &c
}

// orderLike reorders n's mapping keys to follow like's, keys like lacks last.
func orderLike(n, like *yaml.Node) {
	if n == nil || like == nil || n.Kind != like.Kind {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		rank := map[string]int{}
		for i := 0; i+1 < len(like.Content); i += 2 {
			rank[like.Content[i].Value] = i/2 + 1
		}
		type pair struct{ k, v *yaml.Node }
		var ps []pair
		for i := 0; i+1 < len(n.Content); i += 2 {
			ps = append(ps, pair{n.Content[i], n.Content[i+1]})
		}
		slices.SortStableFunc(ps, func(a, b pair) int {
			ra, rb := rank[a.k.Value], rank[b.k.Value]
			if ra == 0 {
				ra = len(rank) + 1
			}
			if rb == 0 {
				rb = len(rank) + 1
			}
			return ra - rb
		})
		n.Content = n.Content[:0]
		for _, p := range ps {
			n.Content = append(n.Content, p.k, p.v)
			orderLike(p.v, get(like, p.k.Value))
		}
	case yaml.SequenceNode:
		// Lay an item out like its equal in like, else like the item in its
		// place, since items come and go.
		for i, x := range n.Content {
			var v any
			_ = x.Decode(&v)
			j := slices.IndexFunc(like.Content, func(y *yaml.Node) bool {
				var w any
				_ = y.Decode(&w)
				return reflect.DeepEqual(v, w)
			})
			switch {
			case j >= 0:
				orderLike(x, like.Content[j])
			case i < len(like.Content):
				orderLike(x, like.Content[i])
			case len(like.Content) > 0:
				orderLike(x, like.Content[len(like.Content)-1])
			}
		}
	}
}

// lineDiff is b against a, line by line, from their longest common
// subsequence.
func lineDiff(a, b string) string {
	x, y := splitLines(a), splitLines(b)
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out = append(out, " "+x[i])
			i, j = i+1, j+1
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, "+"+y[j])
			j++
		default:
			out = append(out, "-"+x[i])
			i++
		}
	}
	return strings.Join(out, "\n")
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func find(objs []Object, kind, name string) (Object, bool) {
	for _, o := range objs {
		if o.Kind == kind && o.Name == name {
			return o, true
		}
	}
	return Object{}, false
}

// RouteInSync is whether the running AIGatewayRoute has the route's rule as
// it is now.
func RouteInSync(running []Object, r model.Route) bool {
	o, ok := find(running, "AIGatewayRoute", RouteName)
	if !ok {
		return false
	}
	var spec struct {
		Rules []any `yaml:"rules"`
	}
	if err := get(o.node, "spec").Decode(&spec); err != nil {
		return false
	}
	var want any
	_ = toNode(compileRule(r)).Decode(&want)
	return slices.ContainsFunc(spec.Rules, func(x any) bool { return reflect.DeepEqual(x, want) })
}

// BackendInSync is whether the gateway runs every resource the backend
// compiles to, as it is now.
func BackendInSync(running []Object, b model.Backend) bool {
	objs := backendObjects(b)
	for _, o := range objs {
		r, ok := find(running, o.Kind, o.Name)
		if !ok || !reflect.DeepEqual(r.value(), o.value()) {
			return false
		}
	}
	return len(objs) > 0
}

var (
	routeNameRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?$`)
	headerNameRE = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// ValidateRoute checks a route against the backends it names and the other
// routes (a route with the same name in routes is the one being replaced).
func ValidateRoute(r model.Route, routes []model.Route, backends []model.Backend) error {
	if !routeNameRE.MatchString(r.Name) {
		return errors.New("name must be lowercase letters, digits, dots and dashes")
	}
	if len(r.Match.Models) == 0 {
		return errors.New("match at least one model")
	}
	for _, m := range r.Match.Models {
		switch i := strings.Index(m, "*"); {
		case strings.TrimSpace(m) == "":
			return errors.New("a model name can't be blank")
		case strings.ContainsAny(m, " \t\n"):
			return fmt.Errorf("model %q can't contain spaces", m)
		case i >= 0 && len(r.Match.Models) > 1:
			return fmt.Errorf("%q must be the route's only model", m)
		case i >= 0 && i != len(m)-1:
			return fmt.Errorf("%q: * only goes at the end", m)
		}
	}
	seen := map[string]bool{}
	for _, h := range r.Match.Headers {
		switch {
		case h.Name == ModelHeader:
			return errors.New("x-ai-eg-model is set by the gateway; match models instead")
		case h.Name == HintHeader:
			return errors.New("x-stargate-backend is Warden's reroute hint")
		case !headerNameRE.MatchString(h.Name):
			return fmt.Errorf("header %q: lowercase letters, digits and dashes", h.Name)
		case h.Value == "":
			return fmt.Errorf("header %s needs a value", h.Name)
		case seen[h.Name]:
			return fmt.Errorf("header %s appears twice", h.Name)
		}
		seen[h.Name] = true
	}

	reachable := func(name string) error {
		i := slices.IndexFunc(backends, func(b model.Backend) bool { return b.Name == name })
		switch {
		case i < 0:
			return fmt.Errorf("no backend %q", name)
		case backends[i].Endpoint == nil:
			return fmt.Errorf("%s has no endpoint, so the gateway can't reach it", name)
		}
		return nil
	}
	if len(r.Targets) == 0 {
		return errors.New("send to at least one backend")
	}
	targets := map[string]bool{}
	for _, t := range r.Targets {
		if err := reachable(t.Backend); err != nil {
			return err
		}
		if targets[t.Backend] {
			return fmt.Errorf("%s is a target twice", t.Backend)
		}
		targets[t.Backend] = true
		if len(r.Targets) > 1 && t.Weight < 1 {
			return errors.New("every target needs a weight of at least 1 when there's more than one")
		}
	}
	fallback := map[string]bool{}
	for _, t := range r.Fallback {
		if err := reachable(t.Backend); err != nil {
			return err
		}
		switch {
		case targets[t.Backend]:
			return fmt.Errorf("%s is already a target", t.Backend)
		case fallback[t.Backend]:
			return fmt.Errorf("%s is in the fallback twice", t.Backend)
		case t.Weight != 0:
			return errors.New("fallback backends are tried in order; they take no weight")
		}
		fallback[t.Backend] = true
	}

	for _, o := range routes {
		if o.Name == r.Name || !sameHeaders(o.Match.Headers, r.Match.Headers) {
			continue
		}
		for _, m := range r.Match.Models {
			if slices.Contains(o.Match.Models, m) {
				return fmt.Errorf("%s is already routed by %q", m, o.Name)
			}
		}
	}
	return nil
}

func sameHeaders(a, b []model.HeaderMatch) bool {
	key := func(hs []model.HeaderMatch) []string {
		var s []string
		for _, h := range hs {
			s = append(s, h.Name+"="+h.Value)
		}
		slices.Sort(s)
		return s
	}
	return slices.Equal(key(a), key(b))
}

// catchAll is a route that takes any model whatever the headers.
func catchAll(r model.Route) bool {
	return len(r.Match.Headers) == 0 && slices.Equal(r.Match.Models, []string{"*"})
}

// InsertAt is where a new route goes in routes: ahead of a catch-all, so it
// keeps catching only what nothing else does (rules with as many header
// matches are tried in order).
func InsertAt(routes []model.Route, r model.Route) int {
	if catchAll(r) {
		return len(routes)
	}
	if i := slices.IndexFunc(routes, catchAll); i >= 0 {
		return i
	}
	return len(routes)
}
