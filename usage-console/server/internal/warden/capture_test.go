package warden

import (
	"encoding/json"
	"strings"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/ingest"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

const captureReqID = "0f8e2a4c-1b2d-4e5f-8a9b-0c1d2e3f4a5b"

// capturing is the demo snapshot with route "gpt-5" (gpt-5-mini, gpt-5.5)
// capturing content, as the gateway runs it.
func capturing() *gateway.Snapshot {
	snap := gateway.DemoSnapshot()
	for i := range snap.Routes {
		snap.Routes[i].CaptureContent = snap.Routes[i].Name == "gpt-5"
	}
	snap.RunningRoutes = snap.Routes
	return snap
}

type capturedContent struct {
	Messages []struct{ Role, Content string } `json:"messages"`
	Response string                           `json:"response"`
	Route    string                           `json:"route"`
}

func playCapture(t *testing.T, snap *gateway.Snapshot, model, user string, respHeaders []string, respChunks ...string) (*fakeStream, []store.CapturedContent) {
	t.Helper()
	k := snap.KeyByID("k1")
	f := &fakeStream{in: []*extprocv3.ProcessingRequest{
		headerMsg(false, "x-stargate-key-id", k.ID, "x-stargate-team", k.Team, "x-stargate-project", k.Project, "x-request-id", captureReqID, "x-data-region", "us"),
		bodyMsg(false, string(body(model, user)), true),
	}}
	if respHeaders != nil {
		f.in = append(f.in, headerMsg(true, respHeaders...))
	}
	for i, c := range respChunks {
		f.in = append(f.in, bodyMsg(true, c, i == len(respChunks)-1))
	}
	var got []store.CapturedContent
	s := newServer(snap)
	s.Capture = func(c store.CapturedContent) { got = append(got, c) }
	if err := s.Process(f); err != nil {
		t.Fatal(err)
	}
	return f, got
}

func contentOf(t *testing.T, c store.CapturedContent) capturedContent {
	t.Helper()
	var out capturedContent
	if err := json.Unmarshal(c.Content, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// §9.2: on a route that captures, the prompt and the response are kept with
// every detected value masked, and the receipt says content was captured.
func TestCapturingRouteKeepsMaskedPromptAndResponse(t *testing.T) {
	resp := `{"choices":[{"index":0,"message":{"role":"assistant","content":"Write to zoe@corp.example, not [EMAIL_1]"}}]}`
	f, got := playCapture(t, capturing(), "gpt-5-mini", "mail ana@example.com, call (555) 123-4567",
		[]string{"content-type", "application/json"}, resp[:40], resp[40:])
	if p := policyOf(t, f.out[1]); !p.ContentCaptured || p.DataRegion != "us" {
		t.Fatalf("policy = %+v", p)
	}
	if len(got) != 1 {
		t.Fatalf("captured %d", len(got))
	}
	c := got[0]
	if c.ReceiptID != ingest.ReceiptID(captureReqID) || c.Tenant != "demo" || c.Route != "gpt-5" {
		t.Errorf("captured = %+v", c)
	}
	body := contentOf(t, c)
	all := string(c.Content)
	for _, leak := range []string{"ana@example.com", "zoe@corp.example", "123-4567"} {
		if strings.Contains(all, leak) {
			t.Errorf("stored %q: %s", leak, all)
		}
	}
	if body.Messages[1].Content != "mail [EMAIL_1], call [PHONE_1]" || body.Route != "gpt-5" {
		t.Errorf("prompt = %+v", body)
	}
	if !strings.HasPrefix(body.Response, "Write to [EMAIL_") {
		t.Errorf("response = %q", body.Response)
	}
}

// With nothing to restore, Warden still needs the response body to keep it.
func TestCaptureAsksForTheResponseBody(t *testing.T) {
	sse := "data: " + `{"choices":[{"delta":{"content":"call (555) 123-4567"}}]}` + "\n\n" + "data: [DONE]\n\n"
	f, got := playCapture(t, capturing(), "gpt-5-mini", "hello", []string{"content-type", "text/event-stream"}, sse)
	if f.out[2].GetModeOverride() != nil {
		t.Fatal("response body skipped on a capturing route")
	}
	if chunkOut(t, f.out[3], sse) != sse {
		t.Error("capture must not change what the caller gets")
	}
	if len(got) != 1 || contentOf(t, got[0]).Response != "call [PHONE_1]" {
		t.Fatalf("captured %+v", got)
	}
}

func TestOtherRoutesCaptureNothing(t *testing.T) {
	f, got := playCapture(t, capturing(), "claude-sonnet-5", "hello", []string{"content-type", "application/json"}, `{}`)
	if len(got) != 0 || policyOf(t, f.out[1]).ContentCaptured {
		t.Fatalf("captured %+v", got)
	}
	if f.out[2].GetModeOverride() == nil {
		t.Error("response body requested with nothing to restore or capture")
	}
}

// Capture follows the routes the gateway runs: a pending route change hasn't
// moved this request yet.
func TestCaptureFollowsTheRunningRoutes(t *testing.T) {
	snap := capturing()
	snap.RunningRoutes = []model.Route{{Name: "older", Match: model.RouteMatch{Models: []string{"gpt-5-mini"}}}}
	if _, got := playCapture(t, snap, "gpt-5-mini", "hello", []string{"content-type", "application/json"}, `{}`); len(got) != 0 {
		t.Fatalf("captured %+v", got)
	}
}

// A refused request on a capturing route is kept too, with no response.
func TestRefusedRequestIsCaptured(t *testing.T) {
	f, got := playCapture(t, capturing(), "gpt-5-mini", "-----BEGIN RSA PRIVATE KEY----- abc", nil)
	if f.out[1].GetImmediateResponse() == nil {
		t.Fatal("not refused")
	}
	if len(got) != 1 || contentOf(t, got[0]).Messages[1].Content != "[PRIVATE_KEY_1] abc" || contentOf(t, got[0]).Response != "" {
		t.Fatalf("captured %+v", got)
	}
}

// The stream can end before the response does (the caller went away): what
// arrived is kept.
func TestCaptureKeepsAPartialResponse(t *testing.T) {
	snap := capturing()
	k := snap.KeyByID("k1")
	f := &fakeStream{in: []*extprocv3.ProcessingRequest{
		headerMsg(false, "x-stargate-key-id", k.ID, "x-stargate-team", k.Team, "x-request-id", captureReqID),
		bodyMsg(false, string(body("gpt-5-mini", "hello")), true),
		headerMsg(true, "content-type", "text/event-stream"),
		bodyMsg(true, "data: "+`{"choices":[{"delta":{"content":"partial"}}]}`+"\n\n", false),
	}}
	var got []store.CapturedContent
	s := newServer(snap)
	s.Capture = func(c store.CapturedContent) { got = append(got, c) }
	if err := s.Process(f); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || contentOf(t, got[0]).Response != "partial" {
		t.Fatalf("captured %+v", got)
	}
	// Kept, but not passed off as the whole reply.
	if !strings.Contains(string(got[0].Content), "the stream ended before the response did") {
		t.Errorf("a cut-off response isn't marked: %s", got[0].Content)
	}
}

// A response with no body ends at its headers: kept whole, not marked cut short.
func TestCaptureOfABodylessResponseIsComplete(t *testing.T) {
	snap := capturing()
	k := snap.KeyByID("k1")
	end := headerMsg(true, "content-type", "application/json")
	end.GetResponseHeaders().EndOfStream = true
	f := &fakeStream{in: []*extprocv3.ProcessingRequest{
		headerMsg(false, "x-stargate-key-id", k.ID, "x-stargate-team", k.Team, "x-request-id", captureReqID),
		bodyMsg(false, string(body("gpt-5-mini", "hello")), true),
		end,
	}}
	var got []store.CapturedContent
	s := newServer(snap)
	s.Capture = func(c store.CapturedContent) { got = append(got, c) }
	if err := s.Process(f); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || strings.Contains(string(got[0].Content), "stream ended") {
		t.Fatalf("captured %+v", got)
	}
}
