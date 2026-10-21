package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// fakeKeys is the key store; tests add keys to it as the key-manager would.
type fakeKeys struct {
	mu   sync.Mutex
	keys []secrets.KeyInfo
}

func (f *fakeKeys) ListAllKeys(context.Context) ([]secrets.KeyInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]secrets.KeyInfo(nil), f.keys...), nil
}

func (f *fakeKeys) add(k secrets.KeyInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, k)
}

func lookupAPI(keys *fakeKeys) http.Handler {
	mux := http.NewServeMux()
	NewHandler(keys).RegisterRoutes(mux)
	return mux
}

func getOwner(t *testing.T, api http.Handler, clientID string) (int, Owner) {
	t.Helper()
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/internal/v1/key-owners/"+clientID, nil))
	var o Owner
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &o); err != nil {
			t.Fatalf("decoding %s: %v", rr.Body.String(), err)
		}
	}
	return rr.Code, o
}

// The usage pipeline only sees an API key's client ID; it asks who minted the
// key so the usage can be billed to that person and their team.
func TestKeyOwnerIsTheUserWhoMintedTheKey(t *testing.T) {
	keys := &fakeKeys{keys: []secrets.KeyInfo{{
		ClientID: "user-alice-model-1", Creator: "alice", CreatorSubject: "sub-alice",
		CreatorGroups: []string{"llm-users", "team-a"},
	}}}

	code, owner := getOwner(t, lookupAPI(keys), "user-alice-model-1")

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if owner.Subject != "sub-alice" || owner.Username != "alice" || strings.Join(owner.Groups, ",") != "llm-users,team-a" {
		t.Errorf("owner = %+v, want alice / sub-alice / [llm-users team-a]", owner)
	}
}

func TestUnknownKeyIsNotFound(t *testing.T) {
	code, _ := getOwner(t, lookupAPI(&fakeKeys{}), "user-nobody-1")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

// A key's first request arrives seconds after it was minted, after the lookup
// has already answered for other keys.
func TestKeyMintedAfterEarlierLookupsIsFound(t *testing.T) {
	keys := &fakeKeys{keys: []secrets.KeyInfo{{ClientID: "user-alice-model-1", Creator: "alice", CreatorSubject: "sub-alice"}}}
	api := lookupAPI(keys)
	if code, _ := getOwner(t, api, "user-alice-model-1"); code != http.StatusOK {
		t.Fatalf("first lookup status = %d, want 200", code)
	}

	keys.add(secrets.KeyInfo{ClientID: "user-bob-model-1", Creator: "bob", CreatorSubject: "sub-bob"})

	if code, owner := getOwner(t, api, "user-bob-model-1"); code != http.StatusOK || owner.Subject != "sub-bob" {
		t.Errorf("lookup of a new key = %d %+v, want 200 with sub-bob", code, owner)
	}
}
