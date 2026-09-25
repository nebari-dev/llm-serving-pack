package ingest

import (
	"testing"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
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
