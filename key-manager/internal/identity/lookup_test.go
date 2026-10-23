package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// fakeKeys is the key store; tests add and revoke keys as the key-manager
// would.
type fakeKeys struct {
	mu    sync.Mutex
	keys  []secrets.KeyInfo
	reads int           // ListAllKeys calls
	delay time.Duration // how long a read takes
}

func (f *fakeKeys) ListAllKeysIncludingRevoked(context.Context) ([]secrets.KeyInfo, error) {
	f.mu.Lock()
	f.reads++
	keys := append([]secrets.KeyInfo(nil), f.keys...)
	delay := f.delay
	f.mu.Unlock()
	time.Sleep(delay)
	return keys, nil
}

func (f *fakeKeys) revoke(clientID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = slices.DeleteFunc(f.keys, func(k secrets.KeyInfo) bool { return k.ClientID == clientID })
}

func (f *fakeKeys) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
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

// The gateway's access log for a key's last requests can reach the pipeline
// after the key is revoked. A client ID is never reissued, so its owner is
// still the person who minted it.
func TestRevokedKeyStillNamesItsOwner(t *testing.T) {
	keys := &fakeKeys{keys: []secrets.KeyInfo{{ClientID: "user-alice-model-1", Creator: "alice", CreatorSubject: "sub-alice"}}}
	api := lookupAPI(keys)
	if code, _ := getOwner(t, api, "user-alice-model-1"); code != http.StatusOK {
		t.Fatalf("lookup before revoke status = %d, want 200", code)
	}

	keys.revoke("user-alice-model-1")
	getOwner(t, api, "user-nobody-1") // an unknown ID makes the lookup re-read the key store

	if code, owner := getOwner(t, api, "user-alice-model-1"); code != http.StatusOK || owner.Subject != "sub-alice" {
		t.Errorf("lookup of a revoked key = %d %+v, want 200 with sub-alice", code, owner)
	}
}

// Every unknown client ID costs a read of every key from the Kubernetes API.
// Lookups arriving together share one read instead of making one each.
func TestConcurrentUnknownLookupsShareOneKeyStoreRead(t *testing.T) {
	keys := &fakeKeys{delay: 50 * time.Millisecond}
	api := lookupAPI(keys)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			getOwner(t, api, "user-nobody-"+strconv.Itoa(i))
		}()
	}
	wg.Wait()

	if reads := keys.readCount(); reads > 2 {
		t.Errorf("50 concurrent unknown lookups read the key store %d times, want at most 2", reads)
	}
}

// Unknown client IDs one after another read the key store at most once per
// interval. A lookup that would have to wait longer than its caller allows
// answers 503, so the caller retries rather than taking the key as unknown.
func TestUnknownLookupsReadTheKeyStoreAtMostOncePerInterval(t *testing.T) {
	keys := &fakeKeys{}
	h := NewHandler(keys)
	h.minInterval = time.Hour
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	if code, _ := getOwner(t, mux, "user-nobody-1"); code != http.StatusNotFound {
		t.Fatalf("first unknown lookup status = %d, want 404", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/internal/v1/key-owners/user-nobody-2", nil).WithContext(ctx))

	if reads := keys.readCount(); reads != 1 {
		t.Errorf("key store read %d times, want 1", reads)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("second unknown lookup status = %d, want 503", rr.Code)
	}
}

// The lookup only remembers owners in memory. After the key manager
// restarts, a key revoked before the restart must still name its owner, as
// the usage of its last requests can be processed later.
func TestKeyRevokedBeforeARestartStillNamesItsOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"llm.nebari.dev/model-name": "m"}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "m-api-keys", Namespace: "ns", Labels: labels}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "m-api-key-metadata", Namespace: "ns", Labels: labels}},
	).Build()
	keys := secrets.NewManager(kube, "ns")
	ctx := context.Background()
	created, err := keys.CreateKey(ctx, "m", secrets.Creator{Username: "alice", Subject: "sub-alice", Groups: []string{"team-a"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.RevokeKey(ctx, "m", created.ClientID); err != nil {
		t.Fatal(err)
	}

	// A new handler: nothing remembered from before the restart.
	mux := http.NewServeMux()
	NewHandler(keys).RegisterRoutes(mux)
	code, owner := getOwner(t, mux, created.ClientID)

	if code != http.StatusOK || owner.Subject != "sub-alice" || owner.Username != "alice" || strings.Join(owner.Groups, ",") != "team-a" {
		t.Errorf("lookup of a key revoked before the restart = %d %+v, want 200 with alice / sub-alice / [team-a]", code, owner)
	}
}
