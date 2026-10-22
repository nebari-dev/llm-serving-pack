package kubesecret_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/kubesecret"
)

// fakeAPI is the slice of the Kubernetes API that creating a Secret uses:
// POST /api/v1/namespaces/<ns>/secrets, 409 when the name is taken.
type fakeAPI struct {
	mu      sync.Mutex
	secrets map[string]map[string]string // "<ns>/<name>" -> stringData
	auth    []string
}

func (f *fakeAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/secrets", func(w http.ResponseWriter, r *http.Request) {
		var s struct {
			Metadata   struct{ Name string } `json:"metadata"`
			StringData map[string]string     `json:"stringData"`
		}
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		key := r.PathValue("ns") + "/" + s.Metadata.Name
		if _, ok := f.secrets[key]; ok {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.secrets[key] = s.StringData
		w.WriteHeader(http.StatusCreated)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureCreatesARandomPasswordOnce(t *testing.T) {
	f := &fakeAPI{secrets: map[string]map[string]string{}}
	c := kubesecret.Client{URL: f.server(t).URL, Token: "sa-token", HTTP: http.DefaultClient}

	created, err := c.Ensure(context.Background(), "om", "creds", "clickhouse-password")
	if err != nil || !created {
		t.Fatalf("first: created %v, err %v", created, err)
	}
	first := f.secrets["om/creds"]["clickhouse-password"]
	if len(first) < 32 {
		t.Fatalf("password %q too short", first)
	}

	created, err = c.Ensure(context.Background(), "om", "creds", "clickhouse-password")
	if err != nil || created {
		t.Fatalf("second: created %v, err %v", created, err)
	}
	if got := f.secrets["om/creds"]["clickhouse-password"]; got != first {
		t.Fatal("an existing password was replaced")
	}
	if f.auth[0] != "Bearer sa-token" {
		t.Fatalf("authorization %q", f.auth[0])
	}
}

func TestEnsureMakesADifferentPasswordPerSecret(t *testing.T) {
	f := &fakeAPI{secrets: map[string]map[string]string{}}
	c := kubesecret.Client{URL: f.server(t).URL, HTTP: http.DefaultClient}
	for _, name := range []string{"a", "b"} {
		if _, err := c.Ensure(context.Background(), "om", name, "k"); err != nil {
			t.Fatal(err)
		}
	}
	if f.secrets["om/a"]["k"] == f.secrets["om/b"]["k"] {
		t.Fatal("two secrets got the same password")
	}
}

func TestEnsureReportsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `secrets is forbidden`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	c := kubesecret.Client{URL: srv.URL, HTTP: http.DefaultClient}
	if _, err := c.Ensure(context.Background(), "om", "creds", "k"); err == nil {
		t.Fatal("no error for a 403")
	}
}
