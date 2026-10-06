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

// memKeys is a KeyStore in memory, to see what a write stored.
type memKeys map[string]string

func (m memKeys) Target() string            { return "memory" }
func (m memKeys) Put(ref, key string) error { m[ref] = key; return nil }
func (m memKeys) Remove(ref string) error   { delete(m, ref); return nil }
func (m memKeys) Get(ref string) (string, bool, error) {
	k, ok := m[ref]
	return k, ok, nil
}

// §7.5.1 "Tested once, then sealed": a provider added with a key that fails
// its test isn't saved, and neither is the key. The refusal (422) is the
// provider's error, the key scrubbed from it. (Store is nil here: a refused
// key must be refused before anything is written.)
func TestAddingAProviderWithAFailingKeySavesNothing(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") + `"}}`))
	}))
	defer provider.Close()
	keys := memKeys{}
	h := (&Server{Tenants: []string{"demo"}, Keys: keys}).Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/demo/backends", strings.NewReader(
		`{"name":"together","provider":"OpenAI-compatible","region":"us-east","baseUrl":"`+provider.URL+`/v1","models":["m1"],"apiKey":"sk-wrong-0123456789xyz"}`)))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s, want 422", w.Code, w.Body)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.Code != "key_test_failed" || !strings.Contains(body.Error.Message, "Incorrect API key provided") || !strings.Contains(body.Error.Message, "not saved") {
		t.Errorf("error = %+v", body.Error)
	}
	if strings.Contains(w.Body.String(), "0123456789xyz") {
		t.Errorf("the key came back: %s", w.Body)
	}
	if len(keys) != 0 {
		t.Errorf("a refused key was stored: %v", keys)
	}
}
