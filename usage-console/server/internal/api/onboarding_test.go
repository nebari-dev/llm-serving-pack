package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Onboarding's "send a test request for me" goes through the real gateway
// with the caller's new key, tagged with a session of its own so the console
// can find its receipt. (Envoy replaces a caller's x-request-id at the edge,
// so the receipt id isn't known up front.)
func TestGatewayTestSendsOneRequestThroughTheGateway(t *testing.T) {
	var auth, session string
	var body map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		auth, session = r.Header.Get("Authorization"), r.Header.Get("X-Session-Id")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Hello there, nice to meet!"}}]}`))
	}))
	defer gw.Close()

	s := &Server{GatewayURL: gw.URL + "/"}
	out, err := s.sendGatewayTest(context.Background(), "ngw_live_abcd_secret", "smollm2")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer ngw_live_abcd_secret" || body["model"] != "smollm2" || !strings.HasPrefix(session, "onboarding-") {
		t.Fatalf("auth=%q model=%v session=%q", auth, body["model"], session)
	}
	if out.Status != 200 || out.SessionID != session || !strings.Contains(out.Reply, "Hello") {
		t.Fatalf("got %+v", out)
	}
}

// A refusal is the gateway's answer, not an error of ours: the console shows
// it with the receipt it produced.
func TestGatewayTestReportsTheGatewaysRefusal(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":{"message":"model gpt-5.5 is not allowed for this key"}}`))
	}))
	defer gw.Close()
	s := &Server{GatewayURL: gw.URL}
	out, err := s.sendGatewayTest(context.Background(), "x", "gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != 403 || out.Error != "model gpt-5.5 is not allowed for this key" || out.SessionID == "" {
		t.Fatalf("got %+v", out)
	}
}

func TestGatewayTestNeedsAGateway(t *testing.T) {
	if _, err := (&Server{}).sendGatewayTest(context.Background(), "x", "m"); err == nil {
		t.Fatal("no gateway configured should be an error")
	}
}
