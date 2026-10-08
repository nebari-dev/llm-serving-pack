package api

import (
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
)

// One row per settled receipt, for a spreadsheet: a value with a comma is
// quoted, no price is "no price" (never 0), and a streaming request (no
// usage yet) is left out.
func TestReceiptsCSV(t *testing.T) {
	cost := 0.00412
	rs := []model.Receipt{
		{ID: "a1", TS: 1791403049138, KeyName: "support-bot", Team: "support", Project: "help, desk", RequestedModel: "test", ResolvedModel: "claude-opus-5-5",
			Backend: "testing-anthropic", Provider: "Anthropic", RouteReason: "alias", Verdict: "allowed", Status: 200, InputTokens: 16, OutputTokens: 11, CostUSD: &cost, DurationMS: 1987},
		{ID: "a2", TS: 1791403050000, KeyName: "local", Team: "research", Project: "p", RequestedModel: "smollm2", ResolvedModel: "smollm2",
			Backend: "local", Provider: "Self-hosted", RouteReason: "explicit", Verdict: "allowed", Status: 503, ErrorCode: "upstream_unavailable", DurationMS: 70},
		{ID: "a3", InFlight: true},
	}
	b, n := receiptsCSV(rs)
	if n != 2 {
		t.Fatalf("rows = %d", n)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if lines[0] != "time_utc,receipt_id,key,team,project,requested_model,model,backend,provider,route_reason,verdict,status,error,input_tokens,output_tokens,cost_usd,duration_ms" {
		t.Errorf("header = %s", lines[0])
	}
	if lines[1] != `2026-10-07T19:57:29.138Z,a1,support-bot,support,"help, desk",test,claude-opus-5-5,testing-anthropic,Anthropic,alias,allowed,200,,16,11,0.00412,1987` {
		t.Errorf("row 1 = %s", lines[1])
	}
	if !strings.Contains(lines[2], ",503,upstream_unavailable,0,0,no price,70") {
		t.Errorf("row 2 = %s", lines[2])
	}
}
