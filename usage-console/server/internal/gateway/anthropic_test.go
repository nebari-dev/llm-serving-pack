package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/routing"
)

func TestAPIOf(t *testing.T) {
	for path, want := range map[string]string{
		"/anthropic/v1/messages":              APIAnthropic,
		"/anthropic/v1/messages/count_tokens": APIAnthropic,
		"/anthropic/v1/models":                APIAnthropic,
		"/v1/chat/completions":                APIOpenAI,
		"/v1/models":                          APIOpenAI,
		"/anthropicish/v1/messages":           APIOpenAI,
		"/anthropic/v1/messages?beta=true":    APIAnthropic,
	} {
		if got := APIOf(path); got != want {
			t.Errorf("APIOf(%q) = %q, want %q", path, got, want)
		}
	}
	// The key check's header is what the compiled routes match.
	if !strings.EqualFold(HeaderAPI, routing.APIHeader) || APIAnthropic != routing.AnthropicAPI {
		t.Errorf("%s: %s doesn't match routing's %s: %s", HeaderAPI, APIAnthropic, routing.APIHeader, routing.AnthropicAPI)
	}
}

func authz(t *testing.T, path string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var cur Current
	cur.Store(DemoSnapshot())
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	(&ExtAuthz{Snap: &cur}).ServeHTTP(w, req)
	return w
}

// The Anthropic SDK sends its key as x-api-key, not a bearer token. The key
// check takes the gateway key either way, names the secret the same, removes
// both headers so the provider never sees it, and marks an Anthropic-style
// request for the routes (HeaderAPI).
func TestExtAuthzTakesAnAnthropicStyleKey(t *testing.T) {
	sec := strings.TrimPrefix(secret("k2"), "Bearer ")
	w := authz(t, "/anthropic/v1/messages", map[string]string{"X-Api-Key": sec, "Anthropic-Version": "2023-06-01"}, `{"model":"gpt-5.5","max_tokens":10,"messages":[]}`)
	h := w.Header()
	if w.Code != 200 || h.Get(HeaderKeyID) != "k2" {
		t.Fatalf("%d %v %s", w.Code, h, w.Body)
	}
	if got, want := h.Get(HeaderSecretID), SecretID(demo.HashSecret(sec)); got != want {
		t.Errorf("secret id %q, want %q", got, want)
	}
	if rm := h.Get("X-Envoy-Auth-Headers-To-Remove"); !strings.Contains(rm, "x-api-key") || !strings.Contains(rm, "authorization") {
		t.Errorf("headers to remove = %q", rm)
	}
	if got := h.Get(HeaderAPI); got != APIAnthropic {
		t.Errorf("%s = %q, want %q", HeaderAPI, got, APIAnthropic)
	}

	// An OpenAI-style request carries no API header, whichever header has the key.
	w = authz(t, "/v1/chat/completions", map[string]string{"X-Api-Key": sec}, `{"model":"gpt-5.5"}`)
	if w.Code != 200 || w.Header().Get(HeaderAPI) != "" {
		t.Errorf("OpenAI-style: %d, %s = %q", w.Code, HeaderAPI, w.Header().Get(HeaderAPI))
	}
	// A bearer token wins over x-api-key when both are sent.
	w = authz(t, "/anthropic/v1/messages", map[string]string{"Authorization": secret("k2"), "X-Api-Key": "sk-ant-not-ours"}, `{"model":"gpt-5.5"}`)
	if w.Code != 200 || w.Header().Get(HeaderKeyID) != "k2" {
		t.Errorf("bearer and x-api-key: %d %v", w.Code, w.Header())
	}
}

// anthropicError is Anthropic's error body: {"type":"error","error":{"type","message"}},
// with our code alongside, so the Anthropic SDK raises its usual errors.
type anthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestExtAuthzAnswersAnthropicStyleCallersInAnthropicsShape(t *testing.T) {
	for _, c := range []struct {
		name, key, body string
		status          int
		kind, code      string
	}{
		{"unknown key", "ngw_live_0000_nope", `{"model":"gpt-5-mini"}`, 401, "authentication_error", "invalid_api_key"},
		{"no key", "", `{"model":"gpt-5-mini"}`, 401, "authentication_error", "invalid_api_key"},
		{"model not allowed", strings.TrimPrefix(secret("k4"), "Bearer "), `{"model":"gpt-5.5"}`, 403, "permission_error", "model_not_allowed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := map[string]string{"Anthropic-Version": "2023-06-01"}
			if c.key != "" {
				h["X-Api-Key"] = c.key
			}
			w := authz(t, "/anthropic/v1/messages", h, c.body)
			var e anthropicError
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || w.Code != c.status || e.Type != "error" || e.Error.Type != c.kind || e.Error.Code != c.code || e.Error.Message == "" {
				t.Errorf("%d %s, want %d %s/%s", w.Code, w.Body, c.status, c.kind, c.code)
			}
			if w.Header().Get("Content-Type") != "application/json" {
				t.Errorf("content-type %q", w.Header().Get("Content-Type"))
			}
		})
	}
	// OpenAI-style callers keep OpenAI's shape.
	w := authz(t, "/v1/chat/completions", nil, `{"model":"gpt-5-mini"}`)
	if !strings.HasPrefix(w.Body.String(), `{"error":{"code":"invalid_api_key"`) {
		t.Errorf("OpenAI-style refusal = %s", w.Body)
	}
}

func TestAPIErrorBody(t *testing.T) {
	for status, kind := range map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error", 404: "not_found_error",
		413: "request_too_large", 429: "rate_limit_error", 500: "api_error", 503: "api_error", 529: "overloaded_error"} {
		var e anthropicError
		if err := json.Unmarshal(ErrorBody(APIAnthropic, status, "c", "m"), &e); err != nil || e.Error.Type != kind || e.Type != "error" {
			t.Errorf("%d: %+v (%v), want %s", status, e, err, kind)
		}
	}
	if got := string(ErrorBody(APIOpenAI, 429, "budget_exceeded", "over")); got != `{"error":{"code":"budget_exceeded","message":"over"}}` {
		t.Errorf("OpenAI body = %s", got)
	}
}
