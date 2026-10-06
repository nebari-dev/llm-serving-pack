package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// §6: an update or delete says which version it's changing. Without it the
// request is refused before anything is read or written (Store is nil here,
// so reaching it would panic).
func TestWritesWithoutIfMatchAre428(t *testing.T) {
	h := (&Server{Tenants: []string{"demo"}}).Handler()
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/demo/aliases/fast", `{"target":"gpt-5-mini"}`},
		{"DELETE", "/api/v1/demo/aliases/fast", ""},
		{"PATCH", "/api/v1/demo/budgets/b1", `{"capUsd":10}`},
		{"DELETE", "/api/v1/demo/budgets/b1", ""},
		{"PUT", "/api/v1/demo/policies/r1/draft", `{"name":"x"}`},
		{"DELETE", "/api/v1/demo/policies/r1/draft", ""},
		{"POST", "/api/v1/demo/policies/r1/publish", `{"mode":"enforce"}`},
		{"POST", "/api/v1/demo/policies/r1/rollback", `{"version":1}`},
		{"DELETE", "/api/v1/demo/policies/r1", ""},
		{"PUT", "/api/v1/demo/backends/together", `{"provider":"OpenAI-compatible"}`},
		{"DELETE", "/api/v1/demo/backends/together", ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)))
		if w.Code != http.StatusPreconditionRequired || !strings.Contains(w.Body.String(), "precondition_required") {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}
