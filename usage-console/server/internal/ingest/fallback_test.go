package ingest

import (
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
)

// withRoutes is the test snapshot with gpt-5-mini routed to openai-prod,
// falling back to azure-openai-eu, and a local model renamed for its server.
func withRoutes() *gateway.Snapshot {
	s := *snap
	s.Backends = append(s.Backends, model.Backend{Name: "local", Provider: "Self-hosted", Region: "local"})
	s.Models = map[string]model.Model{"gpt-5-mini": s.Models["gpt-5-mini"], "smollm2": {ID: "smollm2"}}
	s.Routes = []model.Route{
		{Name: "gpt", Match: model.RouteMatch{Models: []string{"gpt-5-mini"}}, Targets: []model.RouteTarget{{Backend: "openai-prod"}}, Fallback: []model.RouteTarget{{Backend: "azure-openai-eu"}}},
		{Name: "smollm2", Match: model.RouteMatch{Models: []string{"smollm2"}}, Targets: []model.RouteTarget{{Backend: "local", Model: "${LOCAL_LLM_MODEL:-ai/smollm2:360M-Q4_K_M}"}}},
	}
	return &s
}

func routeStep(rc *model.Receipt) model.TraceStep {
	for _, t := range rc.Trace {
		if t.Step == "Route selected" {
			return t
		}
	}
	return model.TraceStep{}
}

// Served by a fallback because the route's target was down: the receipt
// says so, and which backend it fell back from.
func TestReceiptSaysWhenAFallbackServed(t *testing.T) {
	rc, err := Receipt(withRoutes(), record(map[string]string{"gen_ai.provider.name": "default/azure-openai-eu/route/aigw-run/rule/0/ref/1"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.RouteReason != "fallback" || rc.FallbackFrom != "openai-prod" || rc.Backend != "azure-openai-eu" {
		t.Fatalf("reason %q from %q backend %q", rc.RouteReason, rc.FallbackFrom, rc.Backend)
	}
	if st := routeStep(rc); !strings.Contains(st.Outcome, "fallback: openai-prod unavailable") || st.State != "warn" {
		t.Errorf("route step = %+v", st)
	}
}

func TestReceiptFromTheRoutesOwnTargetIsNoFallback(t *testing.T) {
	rc, _ := Receipt(withRoutes(), record(nil))
	if rc.RouteReason != "explicit" || rc.FallbackFrom != "" {
		t.Fatalf("reason %q from %q", rc.RouteReason, rc.FallbackFrom)
	}
}

// A policy's reroute isn't a fallback, even to a backend that's one.
func TestRerouteToAFallbackBackendIsNotAFallback(t *testing.T) {
	p := `{"mode":"enforced","verdict":"rerouted","routeReason":"policy","rules":[],"redactions":[],"trace":[]}`
	rc, _ := Receipt(withRoutes(), record(map[string]string{"gen_ai.provider.name": "default/azure-openai-eu/route/stargate-hints/rule/1/ref/0", "stargate.policy": p}))
	if rc.RouteReason != "policy" || rc.FallbackFrom != "" {
		t.Fatalf("reason %q from %q", rc.RouteReason, rc.FallbackFrom)
	}
}

// A local runner reports its model as a file path; the receipt names it as
// the route sent it.
func TestUpstreamFilePathShowsAsTheModelTheRouteSent(t *testing.T) {
	rc, _ := Receipt(withRoutes(), record(map[string]string{"gen_ai.request.model": "smollm2", "gen_ai.response.model": "/Users/x/.docker/models/bundles/sha256/354b/model/model.gguf",
		"gen_ai.provider.name": "default/local/route/aigw-run/rule/1/ref/0"}))
	st := routeStep(rc)
	if strings.Contains(st.Outcome, "model.gguf") || !strings.Contains(st.Outcome, "upstream calls it ai/smollm2:360M-Q4_K_M") {
		t.Fatalf("route step = %q", st.Outcome)
	}
}

// A route whose backends can't be reached logs no backend either, but it
// matched: Envoy's flags say the upstream failed (UF: connection failure,
// URX: retries exhausted, UH: no healthy host), not NR (no route). The
// receipt says the route's targets were unreachable, not "no matching route".
func TestUnreachableTargetsAreNotNoRoute(t *testing.T) {
	rc, err := Receipt(withRoutes(), record(map[string]string{"response_code": "503", "response_flags": "UF,URX", "gen_ai.provider.name": "-",
		"gen_ai.response.model": "-", "upstream_request_attempt_count": "3"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.ErrorCode != "upstream_unavailable" || rc.Verdict != "allowed" {
		t.Fatalf("code %q verdict %q", rc.ErrorCode, rc.Verdict)
	}
	st := routeStep(rc)
	if strings.Contains(st.Outcome, "no matching route") || !strings.Contains(st.Outcome, "openai-prod unreachable after 3 attempts") {
		t.Errorf("route step = %q", st.Outcome)
	}
	if !strings.Contains(rc.ErrorDetail, "openai-prod") {
		t.Errorf("detail = %q", rc.ErrorDetail)
	}
}
