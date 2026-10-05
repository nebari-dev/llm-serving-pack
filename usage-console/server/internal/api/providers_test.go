package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/routing"
)

// §9.1: provider keys are never returned by the API, not even masked beyond a
// prefix. No response type has a field that could carry one.
func TestNoResponseTypeCarriesAProviderKey(t *testing.T) {
	for _, v := range []any{model.Backend{}, routing.ConnectionTest{}, BackendResult{}, routing.Change{}, RoutingPlan{}} {
		walkFields(t, reflect.TypeOf(v), reflect.TypeOf(v).Name())
	}
}

func walkFields(t *testing.T, ty reflect.Type, path string) {
	for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice {
		ty = ty.Elem()
	}
	if ty.Kind() != reflect.Struct || ty.PkgPath() == "time" {
		return
	}
	for i := range ty.NumField() {
		f := ty.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		switch strings.ToLower(name) {
		case "apikey", "secret", "key", "token", "value", "credential":
			if f.Type.Kind() == reflect.String {
				t.Errorf("%s.%s (json %q) is a string field that could carry a key", path, f.Name, name)
			}
		}
		if f.IsExported() {
			walkFields(t, f.Type, path+"."+f.Name)
		}
	}
}

// Testing an unsaved provider takes the key in the body only. The result
// lists the models, or gives the provider's refusal verbatim, and never the key.
func TestTestUnsavedProvider(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-right-0123456789" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") + `","code":"invalid_api_key"}}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer provider.Close()
	h := (&Server{Tenants: []string{"demo"}}).Handler()
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/demo/backends/test", strings.NewReader(body)))
		return w
	}

	w := post(`{"provider":"OpenAI-compatible","baseUrl":"` + provider.URL + `/v1","apiKey":"sk-wrong-0123456789xyz"}`)
	var got routing.ConnectionTest
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != 200 || err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got.OK || got.Status != 401 || !strings.Contains(got.Error, "Incorrect API key provided") {
		t.Errorf("wrong key: %+v", got)
	}
	if strings.Contains(w.Body.String(), "0123456789xyz") {
		t.Errorf("the key came back: %s", w.Body)
	}

	w = post(`{"provider":"OpenAI-compatible","baseUrl":"` + provider.URL + `/v1","apiKey":"sk-right-0123456789"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.OK || len(got.Models) != 1 || got.Models[0] != "m1" || strings.Contains(w.Body.String(), "sk-right") {
		t.Errorf("right key: %d %s", w.Code, w.Body)
	}

	for _, bad := range []string{
		`{"provider":"Bedrock","baseUrl":"https://bedrock.example/v1"}`,
		`{"provider":"OpenAI","baseUrl":"not a url"}`,
		`{"provider":"OpenAI","baseUrl":"https://api.openai.com/v1","apiKey":"bad\nkey-0123456789"}`,
	} {
		if w := post(bad); w.Code != 400 {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
}
