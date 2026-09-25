package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jbouder/stargate/server/internal/demo"
)

func TestExtAuthz(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	s := DemoSnapshot()
	// k4 is mid-rotation: the old secret works until the overlap ends, the new one after.
	k4 := s.KeyByID("k4")
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	k4.NextHash = demo.HashSecret("ngw_live_9a0c_next")
	s.KeyBy[k4.NextHash] = k4
	k1 := s.KeyByID("k1")
	expired := "2026-09-01"
	k1.ExpiresAt = &expired

	var cur Current
	cur.Store(s)
	a := &ExtAuthz{Snap: &cur, Now: func() time.Time { return now }}

	cases := []struct {
		name, auth, body string
		rotateUntil      *time.Time
		status           int
		code             string
		keyID            string
	}{
		{name: "allowed", auth: secret("k2"), body: `{"model":"gpt-5.5"}`, status: 200, keyID: "k2"},
		{name: "alias resolves before the allowlist", auth: secret("k3"), body: `{"model":"summarize-digest"}`, status: 200, keyID: "k3"},
		{name: "no model only needs a key", auth: secret("k2"), status: 200, keyID: "k2"},
		{name: "missing key", status: 401, code: "invalid_api_key"},
		{name: "unknown key", auth: "Bearer ngw_live_0000_nope", body: `{"model":"gpt-5-mini"}`, status: 401, code: "invalid_api_key"},
		{name: "revoked", auth: secret("k7"), body: `{"model":"gpt-5-mini"}`, status: 401, code: "invalid_api_key"},
		{name: "expired", auth: secret("k1"), body: `{"model":"gpt-5-mini"}`, status: 401, code: "expired_api_key"},
		{name: "old secret during overlap", auth: secret("k4"), body: `{"model":"gpt-5-mini"}`, rotateUntil: &future, status: 200, keyID: "k4"},
		{name: "old secret after overlap", auth: secret("k4"), body: `{"model":"gpt-5-mini"}`, rotateUntil: &past, status: 401, code: "invalid_api_key"},
		{name: "new secret after overlap", auth: "Bearer ngw_live_9a0c_next", body: `{"model":"gpt-5-mini"}`, rotateUntil: &past, status: 200, keyID: "k4"},
		{name: "model not allowed", auth: secret("k4"), body: `{"model":"gpt-5.5"}`, status: 403, code: "model_not_allowed", keyID: "k4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k4.RotateUntil = c.rotateUntil
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(c.body))
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			w := httptest.NewRecorder()
			a.ServeHTTP(w, req)
			if w.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, c.status, w.Body)
			}
			if c.code != "" && !strings.Contains(w.Body.String(), `"code":"`+c.code+`"`) {
				t.Errorf("body = %s, want code %s", w.Body, c.code)
			}
			if got := w.Header().Get(HeaderKeyID); got != c.keyID {
				t.Errorf("%s = %q, want %q", HeaderKeyID, got, c.keyID)
			}
			remove := w.Header().Get("X-Envoy-Auth-Headers-To-Remove")
			if (c.status == http.StatusOK) != strings.Contains(remove, "authorization") {
				t.Errorf("headers to remove = %q", remove)
			}
		})
	}
}

func TestExtAuthzIdentityHeaders(t *testing.T) {
	var cur Current
	cur.Store(DemoSnapshot())
	a := &ExtAuthz{Snap: &cur}

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-1"}`))
	req.Header.Set("Authorization", secret("k4"))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	h := w.Header()
	if w.Code != 403 || h.Get(HeaderTeam) != "web" || h.Get(HeaderProject) != "assistant" || h.Get(HeaderModel) != "claude-opus-4-1" {
		t.Errorf("403 headers: %d %v", w.Code, h)
	}

	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-mini"}`))
	req.Header.Set("Authorization", secret("k4"))
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)
	h = w.Header()
	if w.Code != 200 || h.Get(HeaderTeam) != "web" || h.Get(HeaderProject) != "assistant" || h.Get(HeaderModel) != "" {
		t.Errorf("200 headers: %d %v", w.Code, h)
	}
}
