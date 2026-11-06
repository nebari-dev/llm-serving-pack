package openmeter_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeOpenMeter answers the OpenMeter API calls Bootstrap makes: meters,
// features and price overrides, listed and created, the way OpenMeter does:
// lists are paged (20 a page unless page[size] says otherwise) and prices
// come back as canonical decimals, unset rates as "0".
type fakeOpenMeter struct {
	overrides []map[string]any
	// features overrides the two default features when set; meters the
	// default llm_requests and llm_tokens meters. created holds what POSTs
	// created, patched what PATCHes changed.
	features []map[string]any
	meters   []map[string]any
	created  []map[string]any
	patched  []map[string]any
}

func (f *fakeOpenMeter) server(t *testing.T) *httptest.Server {
	t.Helper()
	if f.features == nil {
		f.features = []map[string]any{feature("f-mock", "mock-provider", "mock"), feature("f-bedrock", "bedrock", "bedrock")}
	}
	if f.meters == nil {
		f.meters = []map[string]any{{"id": "m-req", "key": "llm_requests"}, {"id": "m-tok", "key": "llm_tokens"}}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/openmeter/llm-cost/overrides", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, r, f.overrides)
	})
	mux.HandleFunc("GET /api/v3/openmeter/features", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, r, f.features)
	})
	mux.HandleFunc("GET /api/v3/openmeter/meters", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, r, f.meters)
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
				f.overrides = append(f.overrides, map[string]any{"provider": map[string]any{"id": body["provider"]}, "model": map[string]any{"id": body["model_id"]},
					"pricing": canonicalPricing(body["pricing"].(map[string]any)), "effective_from": body["effective_from"]})
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, body)
		}
	}
	mux.HandleFunc("POST /api/v3/openmeter/features", create("feature"))
	mux.HandleFunc("POST /api/v3/openmeter/llm-cost/overrides", create("override"))
	mux.HandleFunc("PATCH /api/v3/openmeter/features/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		for _, ft := range f.features {
			if ft["id"] == r.PathValue("id") {
				ft["unit_cost"] = body["unit_cost"]
				body["_id"] = ft["id"]
				f.patched = append(f.patched, body)
				writeJSON(w, ft)
				return
			}
		}
		http.NotFound(w, r)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// writePage writes one page of items, as page[number] and page[size] ask.
func writePage(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	size, number := 20, 1
	if v, err := strconv.Atoi(r.URL.Query().Get("page[size]")); err == nil {
		size = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("page[number]")); err == nil {
		number = v
	}
	start := min((number-1)*size, len(items))
	end := min(start+size, len(items))
	page := items[start:end]
	if page == nil {
		page = []map[string]any{}
	}
	writeJSON(w, map[string]any{"data": page, "meta": map[string]any{"page": map[string]any{"number": number, "size": size, "total": len(items)}}})
}

// canonicalPricing is the pricing OpenMeter returns for one it was sent:
// every rate as a plain decimal (3e-6 -> 0.000003), unset ones as "0".
func canonicalPricing(p map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"input_per_token", "output_per_token", "cache_read_per_token", "cache_write_per_token", "reasoning_per_token"} {
		r, ok := new(big.Rat).SetString(fmt.Sprint(p[k]))
		if !ok {
			r = new(big.Rat)
		}
		s := strings.TrimRight(r.FloatString(20), "0")
		out[k] = strings.TrimSuffix(s, ".")
	}
	return out
}

// feature is a priced OpenMeter feature over one model resource's tokens.
func feature(id, resource, provider string) map[string]any {
	return map[string]any{
		"id": id, "key": id,
		"meter":     map[string]any{"filters": map[string]any{"model_resource": map[string]any{"eq": resource}}},
		"unit_cost": map[string]any{"type": "llm", "provider": provider, "model_property": "model", "token_type_property": "token_type"},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
