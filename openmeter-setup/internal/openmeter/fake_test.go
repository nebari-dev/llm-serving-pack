package openmeter_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeOpenMeter answers the OpenMeter API calls Bootstrap makes: meters,
// features and price overrides, listed and created.
type fakeOpenMeter struct {
	overrides []map[string]any
	// features overrides the two default features when set; created holds
	// what POSTs created.
	features []map[string]any
	created  []map[string]any
}

func (f *fakeOpenMeter) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/openmeter/llm-cost/overrides", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": orEmpty(f.overrides)})
	})
	mux.HandleFunc("GET /api/v3/openmeter/features", func(w http.ResponseWriter, r *http.Request) {
		fs := f.features
		if fs == nil {
			fs = []map[string]any{feature("f-mock", "mock-provider", "mock"), feature("f-bedrock", "bedrock", "bedrock")}
		}
		writeJSON(w, map[string]any{"data": fs})
	})
	mux.HandleFunc("GET /api/v3/openmeter/meters", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []map[string]any{{"id": "m-req", "key": "llm_requests"}, {"id": "m-tok", "key": "llm_tokens"}}})
	})
	create := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			body["_kind"] = kind
			f.created = append(f.created, body)
			if kind == "feature" {
				f.features = append(f.features, map[string]any{"id": "f-" + fmt.Sprint(len(f.created)), "key": body["key"], "meter": body["meter"], "unit_cost": body["unit_cost"]})
			} else {
				f.overrides = append(f.overrides, map[string]any{"provider": map[string]any{"id": body["provider"]}, "model": map[string]any{"id": body["model_id"]}, "pricing": body["pricing"], "effective_from": body["effective_from"]})
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, body)
		}
	}
	mux.HandleFunc("POST /api/v3/openmeter/features", create("feature"))
	mux.HandleFunc("POST /api/v3/openmeter/llm-cost/overrides", create("override"))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// feature is a priced OpenMeter feature over one model resource's tokens.
func feature(id, resource, provider string) map[string]any {
	return map[string]any{
		"id": id, "key": id,
		"meter":     map[string]any{"filters": map[string]any{"model_resource": map[string]any{"eq": resource}}},
		"unit_cost": map[string]any{"type": "llm", "provider": provider, "model_property": "model", "token_type_property": "token_type"},
	}
}

func orEmpty(xs []map[string]any) []map[string]any {
	if xs == nil {
		return []map[string]any{}
	}
	return xs
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
