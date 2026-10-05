package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/model"
)

func together() model.Backend {
	return model.Backend{Name: "together", Provider: "OpenAI-compatible", Region: "us-east", Models: []string{"llama-4"},
		Endpoint: &model.BackendEndpoint{Schema: "OpenAI", Prefix: "/v1", Host: "api.together.xyz", Port: "443", TLS: true, APIKeyEnv: "STARGATE_PROVIDER_KEY_TOGETHER"}}
}

// A backend's etag covers what an edit can change, not what's observed
// (health, sync) or the key, which is replaced without If-Match.
func TestBackendETag(t *testing.T) {
	a := together()
	b := together()
	b.Health, b.P50, b.Sync, b.Requests1h = "down", 900, "pending", 12
	b.Key = &model.ProviderKey{Prefix: "sk-tog", SetAt: 1}
	b.LastTest = &model.BackendTest{At: 2, OK: true, Message: "1 model"}
	if BackendETag(a) != BackendETag(b) {
		t.Errorf("etag moved with observed state or the key")
	}
	for name, mut := range map[string]func(*model.Backend){
		"region":   func(x *model.Backend) { x.Region = "eu" },
		"models":   func(x *model.Backend) { x.Models = []string{"llama-4", "llama-5"} },
		"provider": func(x *model.Backend) { x.Provider = "OpenAI" },
		"endpoint": func(x *model.Backend) { e := *x.Endpoint; e.Port = "8443"; x.Endpoint = &e },
	} {
		c := together()
		mut(&c)
		if BackendETag(c) == BackendETag(a) {
			t.Errorf("etag ignores %s", name)
		}
	}
}

// The audit log names a key by its prefix only.
func TestBackendAuditNamesNoKey(t *testing.T) {
	b := together()
	b.Key = &model.ProviderKey{Prefix: "sk-tog", SetAt: 1}
	if got, want := backendSummary(b), "together · OpenAI-compatible · https://api.together.xyz/v1 · llama-4 · key sk-tog…"; got != want {
		t.Errorf("backendSummary = %q, want %q", got, want)
	}
	j, _ := json.Marshal(backendAudit(b))
	for _, want := range []string{`"keyPrefix":"sk-tog"`, `"baseUrl":"https://api.together.xyz/v1"`} {
		if !strings.Contains(string(j), want) {
			t.Errorf("audit row %s lacks %s", j, want)
		}
	}
}
