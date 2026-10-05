package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/model"
)

func TestParseBaseURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want model.BackendEndpoint
	}{
		{"https://api.openai.com/v1", model.BackendEndpoint{Schema: "OpenAI", Prefix: "/v1", Host: "api.openai.com", Port: "443", TLS: true}},
		{"https://openrouter.ai/api/v1/", model.BackendEndpoint{Schema: "OpenAI", Prefix: "/api/v1", Host: "openrouter.ai", Port: "443", TLS: true}},
		{"http://vllm.internal:8000/v1", model.BackendEndpoint{Schema: "OpenAI", Prefix: "/v1", Host: "vllm.internal", Port: "8000"}},
		// localhost is the control plane's machine; a gateway in Docker reaches
		// it by STARGATE_HOST, as the seeded fake backends do.
		{"http://localhost:8090/keyed/v1", model.BackendEndpoint{Schema: "OpenAI", Prefix: "/keyed/v1", Host: "${STARGATE_HOST:-localhost}", Port: "8090"}},
		{"http://127.0.0.1:11434", model.BackendEndpoint{Schema: "OpenAI", Host: "${STARGATE_HOST:-127.0.0.1}", Port: "11434"}},
	} {
		got, err := ParseBaseURL(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseBaseURL(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
		if back := BaseURL(got); strings.TrimSuffix(tc.in, "/") != back {
			t.Errorf("BaseURL(ParseBaseURL(%q)) = %q", tc.in, back)
		}
	}
	for _, bad := range []string{"", "api.openai.com/v1", "ftp://x/v1", "https:///v1", "https://u:p@api.openai.com/v1", "https://api.openai.com/v1?x=1", "https://api.openai.com/v1#f", "http://h:99999/v1", "https://h/v 1"} {
		if _, err := ParseBaseURL(bad); err == nil {
			t.Errorf("ParseBaseURL(%q) accepted it", bad)
		}
	}
}

// A saved endpoint's ${VAR:-default}s resolve from the environment the way
// aigw would, so a test from the control plane reaches what the gateway does.
func TestResolvedBaseURL(t *testing.T) {
	e := *demoBackend("local").Endpoint // ${LOCAL_LLM_HOST:-localhost}:${LOCAL_LLM_PORT:-12434}${LOCAL_LLM_PREFIX:-/engines/v1}
	env := map[string]string{"LOCAL_LLM_PORT": "11434"}
	if got, want := ResolvedBaseURL(e, func(k string) string { return env[k] }), "http://localhost:11434/engines/v1"; got != want {
		t.Errorf("ResolvedBaseURL = %q, want %q", got, want)
	}
	if got, want := BaseURL(e), "http://localhost:12434/engines/v1"; got != want {
		t.Errorf("BaseURL = %q, want %q (the defaults)", got, want)
	}
}

func demoBackend(name string) model.Backend {
	i := slices.IndexFunc(demo.Backends, func(b model.Backend) bool { return b.Name == name })
	return demo.Backends[i]
}

func newBackend() model.Backend {
	e, _ := ParseBaseURL("https://api.together.xyz/v1")
	return model.Backend{Name: "together", Provider: "OpenAI-compatible", Region: "us-east", Models: []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo"}, Endpoint: &e}
}

func TestValidateBackend(t *testing.T) {
	if err := ValidateBackend(newBackend(), nil); err != nil {
		t.Fatalf("a valid backend: %v", err)
	}
	for name, mut := range map[string]func(*model.Backend){
		"name":            func(b *model.Backend) { b.Name = "Together AI" },
		"blank provider":  func(b *model.Backend) { b.Provider = " " },
		"bedrock":         func(b *model.Backend) { b.Provider = "Bedrock" },
		"azure":           func(b *model.Backend) { b.Provider = "Azure" },
		"vertex":          func(b *model.Backend) { b.Provider = "Vertex" },
		"region":          func(b *model.Backend) { b.Region = "" },
		"region spaces":   func(b *model.Backend) { b.Region = "us east" },
		"no models":       func(b *model.Backend) { b.Models = nil },
		"blank model":     func(b *model.Backend) { b.Models = []string{""} },
		"pattern model":   func(b *model.Backend) { b.Models = []string{"gpt-*"} },
		"model twice":     func(b *model.Backend) { b.Models = []string{"a", "a"} },
		"model spaces":    func(b *model.Backend) { b.Models = []string{"a b"} },
		"no endpoint":     func(b *model.Backend) { b.Endpoint = nil },
		"key-suffix name": func(b *model.Backend) { b.Name = strings.Repeat("a", 60) },
	} {
		b := newBackend()
		mut(&b)
		if err := ValidateBackend(b, nil); err == nil {
			t.Errorf("%s: accepted %+v", name, b)
		}
	}
	// A seeded Bedrock backend can still be edited; it just can't be made.
	was := demoBackend("bedrock-eu")
	edit := was
	edit.Models = []string{"claude-haiku-4-5"}
	if err := ValidateBackend(edit, &was); err != nil {
		t.Errorf("editing bedrock-eu: %v", err)
	}
	if err := ValidateBackend(edit, nil); err == nil || !strings.Contains(err.Error(), "cloud credentials") {
		t.Errorf("creating a Bedrock backend: %v, want a cloud-credentials refusal", err)
	}
}

// A backend with a key compiles to a Secret naming only the environment
// variable; the key's version (when it was set) is an annotation, so
// replacing a key is a change to apply even though the key itself is never
// in the config.
func TestKeyReplacedIsAPendingChange(t *testing.T) {
	b := newBackend()
	b.Endpoint.APIKeyEnv = KeyRef(b.Name)
	b.Endpoint.KeyVersion = "2026-10-05T10:00:00.000Z"
	before := Compile([]model.Backend{b}, nil)
	secret, ok := find(before, "Secret", "together-key")
	if !ok {
		t.Fatal("no Secret compiled")
	}
	y := secret.YAML()
	for _, want := range []string{"apiKey: ${STARGATE_PROVIDER_KEY_TOGETHER:-not-set}", "stargate.dev/key-version: \"2026-10-05T10:00:00.000Z\""} {
		if !strings.Contains(y, want) {
			t.Errorf("Secret lacks %q:\n%s", want, y)
		}
	}

	b.Endpoint.KeyVersion = "2026-10-05T11:00:00.000Z"
	after := Compile([]model.Backend{b}, nil)
	cs := Diff(before, after)
	if len(cs) != 1 || cs[0].Kind != "Secret" || cs[0].Name != "together-key" || cs[0].Change != "key replaced" {
		t.Fatalf("changes = %+v, want only Secret/together-key key replaced", cs)
	}
	if !BackendInSync(after, b) || BackendInSync(before, b) {
		t.Errorf("BackendInSync doesn't follow the key version")
	}

	// A key set for the first time on a running backend is the same change.
	b.Endpoint.KeyVersion = ""
	unversioned := Compile([]model.Backend{b}, nil)
	b.Endpoint.KeyVersion = "2026-10-05T11:00:00.000Z"
	if cs := Diff(unversioned, Compile([]model.Backend{b}, nil)); len(cs) != 1 || cs[0].Change != "key replaced" {
		t.Errorf("changes = %+v, want key replaced", cs)
	}
	// A seeded backend with no recorded key version compiles as before.
	if strings.Contains(BackendYAML(demoBackend("openrouter")), "annotations") {
		t.Errorf("openrouter gained an annotation:\n%s", BackendYAML(demoBackend("openrouter")))
	}
}

func TestBackendWithoutKeySendsNone(t *testing.T) {
	e, _ := ParseBaseURL("http://vllm.internal:8000/v1")
	y := BackendYAML(model.Backend{Name: "vllm2", Provider: "Self-hosted", Region: "eu", Models: []string{"m"}, Endpoint: &e})
	for _, no := range []string{"Secret", "BackendSecurityPolicy"} {
		if strings.Contains(y, no) {
			t.Errorf("a keyless backend compiled a %s:\n%s", no, y)
		}
	}
}

func fakeProvider(t *testing.T, want string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("X-Api-Key") != "" {
			got = r.Header.Get("X-Api-Key")
			if r.Header.Get("Anthropic-Version") == "" {
				http.Error(w, `{"error":{"message":"anthropic-version required"}}`, 400)
				return
			}
		}
		if got != want {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + got + `. Find yours at https://example.com.","code":"invalid_api_key"}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": "b-model"}, map[string]any{"id": "a-model"}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTestConnectionListsModels(t *testing.T) {
	srv := fakeProvider(t, "sk-right-0123456789")
	got := TestConnection(context.Background(), http.DefaultClient, "OpenAI-compatible", srv.URL+"/v1", "sk-right-0123456789")
	if !got.OK || got.Status != 200 || !slices.Equal(got.Models, []string{"a-model", "b-model"}) {
		t.Errorf("TestConnection = %+v", got)
	}
	got = TestConnection(context.Background(), http.DefaultClient, "Anthropic", srv.URL+"/v1", "sk-right-0123456789")
	if !got.OK {
		t.Errorf("Anthropic-style auth: %+v", got)
	}
}

// A refusal comes back in the provider's words, with the key taken out
// wherever the provider echoed it.
func TestTestConnectionReportsTheProvidersError(t *testing.T) {
	srv := fakeProvider(t, "sk-right-0123456789")
	key := "sk-wrong-0123456789abcdef"
	got := TestConnection(context.Background(), http.DefaultClient, "OpenAI", srv.URL+"/v1", key)
	if got.OK || got.Status != 401 || !strings.Contains(got.Error, "Incorrect API key provided") || !strings.Contains(got.Error, "invalid_api_key") {
		t.Errorf("TestConnection = %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), key) || strings.Contains(string(b), key[len(key)-6:]) {
		t.Errorf("the key leaked into the result: %s", b)
	}

	if got := TestConnection(context.Background(), http.DefaultClient, "OpenAI", "http://127.0.0.1:1/v1", key); got.OK || got.Error == "" {
		t.Errorf("unreachable: %+v", got)
	}
}

func TestScrub(t *testing.T) {
	key := "sk-proj-abcdefghijklmnop1234"
	for _, msg := range []string{
		"Incorrect API key provided: " + key + ".",
		"Incorrect API key provided: sk-proj-****************1234.",
		"bad key ..." + key[10:],
	} {
		if s := Scrub(msg, key); strings.Contains(s, "1234") || strings.Contains(s, key[10:]) {
			t.Errorf("Scrub(%q) = %q", msg, s)
		}
	}
	if s := Scrub("model not found", key); s != "model not found" {
		t.Errorf("Scrub changed a message without a key: %q", s)
	}
}
