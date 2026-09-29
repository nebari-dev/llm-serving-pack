package ingest

import (
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

var snap = &gateway.Snapshot{
	Tenant:   "demo",
	Models:   map[string]model.Model{"gpt-5-mini": {ID: "gpt-5-mini", InPerM: 1, CachedPerM: 0.5, OutPerM: 4, ReasoningPerM: 4}},
	Backends: []model.Backend{{Name: "openai-prod", Provider: "OpenAI", Region: "us-east"}},
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
		"gen_ai.provider.name":             "default/openai-prod/route/aigw-run/rule/0/ref/0",
		"gen_ai.usage.input_tokens":        "1000",
		"gen_ai.usage.cached_input_tokens": "200",
		"gen_ai.usage.output_tokens":       "500",
		"gen_ai.usage.reasoning_tokens":    "100",
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
	// (800*1 + 200*0.5 + 500*4 + 100*4) / 1e6
	if want := 0.0033; rc.CostUSD < want-1e-12 || rc.CostUSD > want+1e-12 {
		t.Errorf("cost = %v, want %v", rc.CostUSD, want)
	}
}

func TestReceiptUpstreamErrorAfterRetries(t *testing.T) {
	rc, err := Receipt(snap, record(map[string]string{"response_code": "529", "upstream_request_attempt_count": "3", "gen_ai.usage.output_tokens": "-"}))
	if err != nil {
		t.Fatal(err)
	}
	if rc.ErrorCode != "upstream_error" || rc.CostUSD != 0 || rc.OutputTokens != 0 {
		t.Errorf("code=%s cost=%v out=%d", rc.ErrorCode, rc.CostUSD, rc.OutputTokens)
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
	if rc.ErrorCode != "client_disconnected" || rc.CostUSD != 0 {
		t.Errorf("code=%s cost=%v", rc.ErrorCode, rc.CostUSD)
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
	if rc.Verdict != "blocked" || rc.ErrorCode != "model_not_allowed" || rc.Status != 403 || rc.CostUSD != 0 {
		t.Errorf("verdict=%s code=%s status=%d cost=%v", rc.Verdict, rc.ErrorCode, rc.Status, rc.CostUSD)
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
	if len(rc.Rules) != 1 || rc.CostUSD == 0 {
		t.Errorf("rules %v cost %v", rc.Rules, rc.CostUSD)
	}
	var steps []string
	for _, s := range rc.Trace {
		steps = append(steps, s.Step)
	}
	if got := strings.Join(steps, " › "); got != "Identity resolved › Budget checked › Rules evaluated › Route selected › Upstream called" {
		t.Errorf("trace = %s", got)
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
	if rc.Verdict != "blocked" || rc.Status != 429 || rc.ErrorCode != "budget_exceeded" || rc.RequestedModel != "gpt-5-mini" || rc.InputTokens != 42 || rc.CostUSD != 0 {
		t.Errorf("receipt = %+v", rc)
	}
	if len(rc.Trace) != 3 || rc.Trace[1].State != "fail" {
		t.Errorf("trace = %+v", rc.Trace)
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
