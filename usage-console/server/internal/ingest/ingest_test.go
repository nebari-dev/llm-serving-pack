package ingest

import (
	"strings"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/pricing"
	"github.com/jbouder/stargate/server/internal/store"
)

func per(v ...float64) (r pricing.Rates) {
	for i := range v {
		r[i] = &v[i]
	}
	return r
}

var priceFrom = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

var snap = &gateway.Snapshot{
	Tenant: "demo",
	Models: map[string]model.Model{"gpt-5-mini": {ID: "gpt-5-mini", Display: "GPT-5 mini", Provider: "OpenAI"}},
	Prices: map[gateway.Pair][]store.PriceRow{
		{Model: "gpt-5-mini", Backend: "openai-prod"}: {{ModelID: "gpt-5-mini", Backend: "openai-prod", Rates: per(1, 0.5, 2, 4, 4),
			Sources: pricing.Sources{"litellm", "litellm", "litellm", "manual", "litellm"}, From: priceFrom}},
		{Model: "gpt-5-mini", Backend: "azure-openai-eu"}: {{ModelID: "gpt-5-mini", Backend: "azure-openai-eu", Rates: per(2, 1, 2, 8, 8), From: priceFrom}},
	},
	Backends: []model.Backend{{Name: "openai-prod", Provider: "OpenAI", Region: "us-east"}, {Name: "azure-openai-eu", Provider: "Azure", Region: "eu-west"}, {Name: "vllm-internal", Provider: "Self-hosted", Region: "eu-private"}},
}

// cost is a receipt's cost, -1 when it has none.
func cost(rc *model.Receipt) float64 {
	if rc.CostUSD == nil {
		return -1
	}
	return *rc.CostUSD
}

// A record as Envoy sends it: every value a string, "-" for unset.
func record(over map[string]string) map[string]string {
	a := map[string]string{
		"start_time": "2026-09-25T15:24:18.450Z", "duration": "868", "response_duration": "120",
		"response_code": "200", "response_flags": "-", "upstream_request_attempt_count": "1",
		"x-request-id":         "adadfbf5-fa1a-4d60-a46e-4e6bf151a8ef",
		"traceparent":          "00-db1ed558c800842868474eda91ecbf61-85adba6525e01f9a-01",
		"session.id":           "-",
		"gen_ai.request.model": "gpt-5-mini", "gen_ai.response.model": "gpt-5-mini",
		"gen_ai.provider.name":                     "default/openai-prod/route/aigw-run/rule/0/ref/0",
		"gen_ai.usage.input_tokens":                "1000",
		"gen_ai.usage.cached_input_tokens":         "200",
		"gen_ai.usage.output_tokens":               "500",
		"gen_ai.usage.reasoning_tokens":            "100",
		"gen_ai.usage.cache_creation_input_tokens": "100",
	}
	for k, v := range over {
		a[k] = v
	}
	return a
}

func TestReceiptAllowed(t *testing.T) {
	rc, err := Receipt(snap, record(nil))
	if err != nil {
		t.Fatal(err)
	}
	if rc.ID != "adadfbf5-fa1a" || rc.TraceID != "db1ed558c800842868474eda91ecbf61" || rc.TS != 1790349858450 {
		t.Errorf("identity fields: id=%s trace=%s ts=%d", rc.ID, rc.TraceID, rc.TS)
	}
	if rc.Backend != "openai-prod" || rc.Provider != "OpenAI" || rc.Region != "us-east" {
		t.Errorf("backend: %s %s %s", rc.Backend, rc.Provider, rc.Region)
	}
	if rc.SessionID != "" || rc.TTFTMS == nil || *rc.TTFTMS != 120 || rc.Verdict != "allowed" {
		t.Errorf("session=%q ttft=%v verdict=%s", rc.SessionID, rc.TTFTMS, rc.Verdict)
	}
	// (700*1 + 200*0.5 + 100*2 + 400*4 + 100*4) / 1e6: cache reads and
	// writes are part of the 1000 input tokens, the 100 reasoning part of
	// the 500 output.
	if want := 0.003; cost(rc) < want-1e-12 || cost(rc) > want+1e-12 {
		t.Errorf("cost = %v, want %v", cost(rc), want)
	}
	if rc.CacheWriteTokens != 100 {
		t.Errorf("cache writes = %d", rc.CacheWriteTokens)
	}
	b := rc.CostBasis
	if b == nil || b.ID != "gpt-5-mini" || b.Display != "GPT-5 mini" || b.Backend != "openai-prod" || b.EffectiveFrom != priceFrom.UnixMilli() ||
		*b.CacheWritePerM != 2 || *b.OutPerM != 4 || b.Sources["output"] != "manual" || b.Sources["input"] != "litellm" {
		t.Errorf("cost basis = %+v", b)
	}
}

func TestReceiptIsPricedForTheBackendThatServedIt(t *testing.T) {
	rc, _ := Receipt(snap, record(map[string]string{"gen_ai.provider.name": "default/azure-openai-eu/route/aigw-run/rule/0/ref/0"}))
	if want := (700*2 + 200*1 + 100*2 + 400*8 + 100*8) / 1e6; cost(rc) < want-1e-12 || cost(rc) > want+1e-12 {
		t.Errorf("cost = %v, want %v", cost(rc), want)
	}
}

func TestReceiptWithNoPriceHasNoCost(t *testing.T) {
	rc, _ := Receipt(snap, record(map[string]string{"gen_ai.provider.name": "default/vllm-internal/route/aigw-run/rule/0/ref/0"}))
	if rc.Status != 200 || rc.CostUSD != nil || rc.CostBasis != nil {
		t.Errorf("an unpriced pair has no cost, not $0: cost=%v basis=%+v", rc.CostUSD, rc.CostBasis)
	}
}

// A receipt is priced at the row in effect when the request started, not
// whichever row the config snapshot had when the record arrived (spec §5.1).
func TestReceiptIsPricedAtItsOwnTime(t *testing.T) {
	change := time.Date(2026, 9, 25, 15, 24, 18, 0, time.UTC)
	pair := gateway.Pair{Model: "gpt-5-mini", Backend: "openai-prod"}
	s := *snap
	s.Prices = map[gateway.Pair][]store.PriceRow{pair: {
		{ModelID: "gpt-5-mini", Backend: "openai-prod", Rates: per(1, 1, 1, 1, 1), From: priceFrom, To: &change},
		{ModelID: "gpt-5-mini", Backend: "openai-prod", Rates: per(2, 2, 2, 2, 2), From: change},
	}}
	tok := 700.0 + 200 + 100 + 400 + 100 // reasoning is inside output
	for _, c := range []struct {
		name string
		at   time.Time
		rate float64
		from time.Time
	}{
		{"1s before the change", change.Add(-time.Second), 1, priceFrom},
		{"1s after the change", change.Add(time.Second), 2, change},
	} {
		rc, err := Receipt(&s, record(map[string]string{"start_time": c.at.Format(time.RFC3339Nano)}))
		if err != nil {
			t.Fatal(err)
		}
		if want := tok * c.rate / 1e6; cost(rc) < want-1e-12 || cost(rc) > want+1e-12 {
			t.Errorf("%s: cost = %v, want %v", c.name, cost(rc), want)
		}
		if rc.CostBasis == nil || rc.CostBasis.EffectiveFrom != c.from.UnixMilli() {
			t.Errorf("%s: cost basis = %+v, want the row from %s", c.name, rc.CostBasis, c.from)
		}
	}
	// Before the pair's first price there's none.
	rc, _ := Receipt(&s, record(map[string]string{"start_time": priceFrom.Add(-time.Second).Format(time.RFC3339Nano)}))
	if rc.CostUSD != nil {
		t.Errorf("before any price: cost = %v, want none", *rc.CostUSD)
	}
}

func TestReceiptUpstreamErrorAfterRetries(t *testing.T) {
	rc, err := Receipt(snap, record(map[string]string{"response_code": "529", "upstream_request_attempt_count": "3", "gen_ai.usage.output_tokens": "-"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.ErrorCode != "upstream_error" || cost(rc) != 0 || rc.OutputTokens != 0 {
		t.Errorf("code=%s cost=%v out=%d", rc.ErrorCode, cost(rc), rc.OutputTokens)
	}
	if rc.Trace[1].State != "warn" || rc.Trace[2].State != "fail" {
		t.Errorf("trace states: %+v", rc.Trace)
	}
}

func TestReceiptNoRoute(t *testing.T) {
	rc, err := Receipt(snap, record(map[string]string{"response_code": "404", "gen_ai.provider.name": "-", "gen_ai.request.model": "nope", "gen_ai.response.model": "-"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Verdict != "blocked" || rc.ErrorCode != "no_route" || rc.ResolvedModel != "nope" {
		t.Errorf("verdict=%s code=%s resolved=%s", rc.Verdict, rc.ErrorCode, rc.ResolvedModel)
	}
}

func TestReceiptRejectsRecordWithoutRequestID(t *testing.T) {
	if _, err := Receipt(snap, record(map[string]string{"x-request-id": "-"})); err == nil {
		t.Error("want error")
	}
}

func TestReceiptTraceIDFallsBackToRequestID(t *testing.T) {
	rc, _ := Receipt(snap, record(map[string]string{"traceparent": "-"}))
	if rc.TraceID != "adadfbf5fa1a4d60a46e4e6bf151a8ef" {
		t.Errorf("trace = %s", rc.TraceID)
	}
}

func TestReceiptClientDisconnect(t *testing.T) {
	rc, _ := Receipt(snap, record(map[string]string{"response_code": "0", "response_flags": "DC"}))
	if rc.ErrorCode != "client_disconnected" || cost(rc) != 0 {
		t.Errorf("code=%s cost=%v", rc.ErrorCode, cost(rc))
	}
}

var keyed = func() *gateway.Snapshot {
	s := *snap
	k := &store.KeyRecord{APIKey: model.APIKey{ID: "k4", Name: "web-chat", Prefix: "ngw_live_9a0c", Team: "web", Project: "assistant", AllowedModels: []string{"gpt-5-mini"}}}
	s.KeyBy = map[string]*store.KeyRecord{"h": k}
	s.Aliases = map[string]string{"summarize-*": "gpt-5-mini"}
	return &s
}()

func TestReceiptIdentityFromKeyCheck(t *testing.T) {
	rc, err := Receipt(keyed, record(map[string]string{"stargate.key_id": "k4", "stargate.team": "web", "stargate.project": "assistant"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.KeyID != "k4" || rc.KeyName != "web-chat" || rc.Team != "web" || rc.Project != "assistant" || rc.Verdict != "allowed" {
		t.Errorf("identity: %s %s %s %s %s", rc.KeyID, rc.KeyName, rc.Team, rc.Project, rc.Verdict)
	}
	if rc.Trace[0].State != "ok" || rc.Trace[0].Input != "Bearer ngw_live_9a0c…" {
		t.Errorf("identity step: %+v", rc.Trace[0])
	}
}

func TestReceiptModelBlockedByKeyCheck(t *testing.T) {
	// ext_authz answered before routing: no model header, no backend, no usage.
	rc, err := Receipt(keyed, record(map[string]string{
		"response_code": "403", "response_flags": "UAEX", "upstream_request_attempt_count": "0",
		"gen_ai.request.model": "-", "gen_ai.response.model": "-", "gen_ai.provider.name": "-",
		"stargate.key_id": "-", "stargate.denied_key_id": "k4", "stargate.denied_model": "gpt-5.5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Verdict != "blocked" || rc.ErrorCode != "model_not_allowed" || rc.Status != 403 || cost(rc) != 0 {
		t.Errorf("verdict=%s code=%s status=%d cost=%v", rc.Verdict, rc.ErrorCode, rc.Status, cost(rc))
	}
	if rc.KeyName != "web-chat" || rc.Team != "web" || rc.Project != "assistant" || rc.RequestedModel != "gpt-5.5" {
		t.Errorf("identity: %s %s %s model=%s", rc.KeyName, rc.Team, rc.Project, rc.RequestedModel)
	}
	if rc.ErrorDetail != "Key web-chat may not call gpt-5.5. Allowed: gpt-5-mini." {
		t.Errorf("detail = %q", rc.ErrorDetail)
	}
}

func TestReceiptTakesWardensDecision(t *testing.T) {
	p := `{"mode":"enforced","verdict":"rerouted","requestedModel":"claude-opus-4-1","routeReason":"policy",` +
		`"rules":[{"ruleId":"r5","name":"cost-guard-opus","version":2,"matched":true,"action":"route to"}],"redactions":[],` +
		`"requestHash":"sha256:ab","trace":[{"step":"Budget checked","state":"ok"},{"step":"Rules evaluated","state":"warn"}]}`
	rc, err := Receipt(snap, record(map[string]string{"stargate.key_id": "k3", "stargate.team": "batch", "stargate.project": "p", "stargate.policy": p}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Verdict != "rerouted" || rc.RequestedModel != "claude-opus-4-1" || rc.ResolvedModel != "gpt-5-mini" || rc.RouteReason != "policy" || rc.RequestHash != "sha256:ab" {
		t.Errorf("receipt = %+v", rc)
	}
	if len(rc.Rules) != 1 || cost(rc) <= 0 {
		t.Errorf("rules %v cost %v", rc.Rules, cost(rc))
	}
	var steps []string
	for _, s := range rc.Trace {
		steps = append(steps, s.Step)
	}
	if got := strings.Join(steps, " › "); got != "Identity resolved › Budget checked › Rules evaluated › Route selected › Upstream called" {
		t.Errorf("trace = %s", got)
	}
}

// Rehydration happens on the way back, so its step follows the upstream call,
// and the count stays on the redaction.
func TestReceiptPutsRehydrationAfterTheUpstreamCall(t *testing.T) {
	p := `{"mode":"enforced","verdict":"redacted","rules":[],"redactions":[{"type":"email","count":2,"rehydrated":1}],` +
		`"trace":[{"step":"Budget checked","state":"ok"},{"step":"Rules evaluated","state":"warn"},{"step":"Placeholders rehydrated","outcome":"restored 1 email","state":"ok"}]}`
	rc, err := Receipt(snap, record(map[string]string{"stargate.key_id": "k1", "stargate.team": "support", "stargate.project": "p", "stargate.policy": p}))
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, s := range rc.Trace {
		steps = append(steps, s.Step)
	}
	if got := strings.Join(steps, " › "); got != "Identity resolved › Budget checked › Rules evaluated › Route selected › Upstream called › Placeholders rehydrated" {
		t.Errorf("trace = %s", got)
	}
	if len(rc.Redactions) != 1 || rc.Redactions[0].Rehydrated != 1 {
		t.Errorf("redactions = %+v", rc.Redactions)
	}
}

func TestReceiptBlockedByWarden(t *testing.T) {
	p := `{"mode":"enforced","verdict":"blocked","requestedModel":"gpt-5-mini","rules":[],"redactions":[],` +
		`"trace":[{"step":"Budget checked","state":"fail"},{"step":"Rules evaluated","state":"skip"}],` +
		`"blocked":{"status":429,"errorCode":"budget_exceeded","errorDetail":"over cap","resolvedModel":"gpt-5-mini","backend":"openai-prod","provider":"OpenAI","region":"us-east","inputTokens":42}}`
	rc, err := Receipt(snap, record(map[string]string{"response_code": "429", "gen_ai.request.model": "-", "gen_ai.response.model": "-",
		"gen_ai.provider.name": "-", "stargate.key_id": "k4", "stargate.team": "web", "stargate.project": "p", "stargate.policy": p}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Verdict != "blocked" || rc.Status != 429 || rc.ErrorCode != "budget_exceeded" || rc.RequestedModel != "gpt-5-mini" || rc.InputTokens != 42 || cost(rc) != 0 {
		t.Errorf("receipt = %+v", rc)
	}
	if len(rc.Trace) != 3 || rc.Trace[1].State != "fail" {
		t.Errorf("trace = %+v", rc.Trace)
	}
}

// A throttle refusal keeps its own verdict, so the aggregates count it apart
// from blocks.
func TestReceiptThrottledByWarden(t *testing.T) {
	p := `{"mode":"enforced","verdict":"throttled","requestedModel":"gpt-5-mini","rules":[],"redactions":[],` +
		`"trace":[{"step":"Budget checked","state":"throttle"},{"step":"Rules evaluated","state":"skip"}],` +
		`"blocked":{"status":429,"errorCode":"budget_throttled","errorDetail":"throttled","resolvedModel":"gpt-5-mini","backend":"openai-prod","provider":"OpenAI","region":"us-east","inputTokens":42}}`
	rc, err := Receipt(snap, record(map[string]string{"response_code": "429", "gen_ai.request.model": "-", "gen_ai.response.model": "-",
		"gen_ai.provider.name": "-", "stargate.key_id": "k4", "stargate.team": "web", "stargate.project": "p", "stargate.policy": p}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Verdict != "throttled" || rc.Status != 429 || rc.ErrorCode != "budget_throttled" || rc.Trace[1].State != "throttle" {
		t.Errorf("receipt = %+v", rc)
	}
}

// Receipts carry the project's id (§5.1), so Spend and Traffic tell two
// teams' same-named projects apart. The key's project wins (a key never
// changes project); a key the snapshot doesn't have yet uses the key check's
// headers, whose name is URL-escaped since names may have spaces.
func TestReceiptRecordsTheProjectID(t *testing.T) {
	s := *keyed
	k := *s.KeyBy["h"]
	k.ProjectID = "p4a1b2c3d"
	s.KeyBy = map[string]*store.KeyRecord{"h": &k}
	rc, err := Receipt(&s, record(map[string]string{"stargate.key_id": "k4", "stargate.team": "web", "stargate.project": "stale", "stargate.project_id": "pstale"}))
	if err != nil || rc.ProjectID != "p4a1b2c3d" || rc.Project != "assistant" {
		t.Fatalf("known key: project %q %q, err %v", rc.ProjectID, rc.Project, err)
	}
	rc, err = Receipt(&s, record(map[string]string{"stargate.key_id": "k99", "stargate.team": "web", "stargate.project": "Help%20desk", "stargate.project_id": "p0f0f0f0f"}))
	if err != nil || rc.ProjectID != "p0f0f0f0f" || rc.Project != "Help desk" {
		t.Fatalf("new key: project %q %q, err %v", rc.ProjectID, rc.Project, err)
	}
	// A 403 from the key check takes the key's project too.
	rc, _ = Receipt(&s, record(map[string]string{"response_code": "403", "gen_ai.request.model": "-", "gen_ai.response.model": "-", "gen_ai.provider.name": "-",
		"stargate.key_id": "-", "stargate.denied_key_id": "k4", "stargate.denied_model": "gpt-5.5"}))
	if rc.ProjectID != "p4a1b2c3d" {
		t.Fatalf("denied: project %q", rc.ProjectID)
	}
}

func TestReceiptRecordsWhichSecret(t *testing.T) {
	rc, err := Receipt(keyed, record(map[string]string{"stargate.key_id": "k4", "stargate.team": "web", "stargate.project": "assistant", "stargate.secret_id": "3f9a0c11b2de"}))
	if err != nil || rc.SecretID != "3f9a0c11b2de" {
		t.Fatalf("secret id %q, err %v", rc.SecretID, err)
	}
	rc, _ = Receipt(keyed, record(map[string]string{"stargate.key_id": "k4", "stargate.team": "web", "stargate.project": "assistant", "stargate.secret_id": "-"}))
	if rc.SecretID != "" {
		t.Fatalf("unset secret id recorded as %q", rc.SecretID)
	}
}

// Real upstreams answer with their own name for a model: OpenRouter says
// "openai/gpt-5-mini", Docker Model Runner the GGUF file's path. The receipt
// keeps the catalog model the request resolved to, so it's priced and
// charted as that, and the trace shows what the upstream called it.
func TestReceiptKeepsTheCatalogModelWhenTheUpstreamNamesItsOwn(t *testing.T) {
	for _, upstream := range []string{"openai/gpt-5-mini", "/Users/x/.docker/models/bundles/sha256/354b/model/model.gguf"} {
		rc, err := Receipt(snap, record(map[string]string{"gen_ai.response.model": upstream}))
		if err != nil {
			t.Fatal(err)
		}
		if rc.ResolvedModel != "gpt-5-mini" || cost(rc) <= 0 {
			t.Errorf("%s: resolved %q, cost %v", upstream, rc.ResolvedModel, cost(rc))
		}
		route := rc.Trace[len(rc.Trace)-2]
		if route.Step != "Route selected" || !strings.Contains(route.Outcome, "upstream calls it "+upstream) {
			t.Errorf("%s: route step = %+v", upstream, route)
		}
	}
}

// A catalog model the upstream names is what served the request, e.g. a
// fallback to another model.
func TestReceiptTakesTheUpstreamsCatalogModel(t *testing.T) {
	s := *snap
	s.Models = map[string]model.Model{"gpt-5-mini": {ID: "gpt-5-mini"}, "gpt-5.5": {ID: "gpt-5.5"}}
	rc, _ := Receipt(&s, record(map[string]string{"gen_ai.request.model": "gpt-5.5"}))
	if rc.ResolvedModel != "gpt-5-mini" {
		t.Errorf("resolved %q", rc.ResolvedModel)
	}
}

// The gateway's own time on a request (spec G6): from the whole request in
// hand to the first byte upstream. Unset when nothing was sent upstream.
func TestReceiptRecordsGatewayOverhead(t *testing.T) {
	rc, _ := Receipt(snap, record(map[string]string{"stargate.overhead_us": "2140"}))
	if rc.OverheadUS == nil || *rc.OverheadUS != 2140 {
		t.Errorf("overhead = %v", rc.OverheadUS)
	}
	rc, _ = Receipt(snap, record(map[string]string{"stargate.overhead_us": "-"}))
	if rc.OverheadUS != nil {
		t.Errorf("no upstream request, overhead = %v", *rc.OverheadUS)
	}
}

// Anthropic-style callers reach an Anthropic backend through its native
// twin (routing.NativeSuffix), which Agent Router logs as the backend. The
// receipt names the backend the console knows, priced as its own, and the
// trace says the request went to Anthropic's Messages API.
func TestReceiptNativeTwinIsItsBackend(t *testing.T) {
	s := *snap
	s.Models = map[string]model.Model{"claude-echo": {ID: "claude-echo", Provider: "Anthropic"}}
	s.Prices = map[gateway.Pair][]store.PriceRow{{Model: "claude-echo", Backend: "claude"}: {{ModelID: "claude-echo", Backend: "claude", Rates: per(3, 0.3, 3.75, 15, 15), From: priceFrom}}}
	s.Backends = []model.Backend{{Name: "claude", Provider: "Anthropic", Region: "us-east"}, {Name: "odd-native", Provider: "OpenAI", Region: "local"}}
	rc, err := Receipt(&s, record(map[string]string{
		"gen_ai.request.model": "claude-echo", "gen_ai.response.model": "claude-echo",
		"gen_ai.provider.name":      "default/claude-native/route/aigw-run-anthropic/rule/0/ref/0",
		"gen_ai.usage.input_tokens": "1000", "gen_ai.usage.cached_input_tokens": "0", "gen_ai.usage.output_tokens": "500",
		"gen_ai.usage.reasoning_tokens": "0", "gen_ai.usage.cache_creation_input_tokens": "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Backend != "claude" || rc.Provider != "Anthropic" || rc.Region != "us-east" {
		t.Errorf("backend: %s %s %s", rc.Backend, rc.Provider, rc.Region)
	}
	if got, want := cost(rc), (1000*3+500*15)/1e6; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("cost = %v, want %v", got, want)
	}
	var route model.TraceStep
	for _, st := range rc.Trace {
		if st.Step == "Route selected" {
			route = st
		}
	}
	if !strings.Contains(route.Outcome, "claude-echo via claude") || !strings.Contains(route.Outcome, "Anthropic Messages API") {
		t.Errorf("route step = %+v", route)
	}

	// A backend that really is named …-native is itself.
	rc, _ = Receipt(&s, record(map[string]string{"gen_ai.provider.name": "default/odd-native/route/aigw-run/rule/0/ref/0"}))
	if rc.Backend != "odd-native" || rc.Provider != "OpenAI" {
		t.Errorf("odd-native: %s %s", rc.Backend, rc.Provider)
	}
}
