package openmeter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// fakeOpenMeter serves the slice of OpenMeter's v1 customer API the sync uses:
// GET, POST and PUT on /api/v1/customers, keyed by customer key.
type fakeOpenMeter struct {
	mu        sync.Mutex
	customers map[string]customer // by key
	writes    int                 // POST + PUT requests served
	fail      bool                // answer every request with 500
}

func newFakeOpenMeter(t *testing.T) (*fakeOpenMeter, *Client) {
	t.Helper()
	f := &fakeOpenMeter{customers: map[string]customer{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, NewClient(srv.URL, "", srv.Client())
}

func (f *fakeOpenMeter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	key, _ := strings.CutPrefix(r.URL.Path, "/api/v1/customers")
	key = strings.TrimPrefix(key, "/")
	switch {
	case r.Method == http.MethodGet && key != "":
		c, ok := f.customers[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(c)
	case r.Method == http.MethodPost && key == "":
		var c customer
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.customers[c.Key] = c
		f.writes++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(c)
	case r.Method == http.MethodPut && key != "":
		var c customer
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.customers[key] = c
		f.writes++
		_ = json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
	}
}

func (f *fakeOpenMeter) subjectKeys(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := slices.Clone(f.customers[key].UsageAttribution.SubjectKeys)
	slices.Sort(keys)
	return keys
}

type fakeKeys []secrets.KeyInfo

func (k fakeKeys) ListAllKeys(context.Context) ([]secrets.KeyInfo, error) { return k, nil }

func key(clientID, creator, subject string) secrets.KeyInfo {
	return secrets.KeyInfo{ClientID: clientID, Creator: creator, CreatorSubject: subject}
}

// Usage made with an API key arrives at OpenMeter under the key's client ID.
// The sync attaches every client ID to its creator's customer, next to the
// creator's own subject (used by token requests), so all of a user's usage
// lands on one customer.
func TestKeysAreAttributedToTheirCreatorsCustomer(t *testing.T) {
	om, client := newFakeOpenMeter(t)
	sync := NewCustomerSync(fakeKeys{
		key("user-alice-a-1", "alice", "sub-alice"),
		key("user-alice-b-1", "alice", "sub-alice"),
		key("user-bob-a-1", "bob", "sub-bob"),
	}, client, nil)

	if err := sync.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := om.subjectKeys("sub-alice"), []string{"sub-alice", "user-alice-a-1", "user-alice-b-1"}; !slices.Equal(got, want) {
		t.Errorf("alice's customer subject keys = %v, want %v", got, want)
	}
	if got, want := om.subjectKeys("sub-bob"), []string{"sub-bob", "user-bob-a-1"}; !slices.Equal(got, want) {
		t.Errorf("bob's customer subject keys = %v, want %v", got, want)
	}
	if name := om.customers["sub-alice"].Name; name != "alice" {
		t.Errorf("alice's customer name = %q, want alice", name)
	}
}

// A customer may already exist, created by whoever runs OpenMeter, with
// subject keys the pack does not know about. They must survive the sync.
func TestExistingCustomerKeepsItsOtherSubjectKeys(t *testing.T) {
	om, client := newFakeOpenMeter(t)
	om.customers["sub-alice"] = customer{
		ID: "01CUSTOMER", Key: "sub-alice", Name: "Alice (billing name)",
		UsageAttribution: usageAttribution{SubjectKeys: []string{"sub-alice", "batch-job-7"}},
	}

	err := NewCustomerSync(fakeKeys{key("user-alice-a-1", "alice", "sub-alice")}, client, nil).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := om.subjectKeys("sub-alice"), []string{"batch-job-7", "sub-alice", "user-alice-a-1"}; !slices.Equal(got, want) {
		t.Errorf("subject keys = %v, want %v", got, want)
	}
	if name := om.customers["sub-alice"].Name; name != "Alice (billing name)" {
		t.Errorf("customer name = %q, the sync must not rename an existing customer", name)
	}
}

// Keys minted before creators' subjects were recorded have no stable owner
// identity; a customer keyed by a username could later belong to someone else.
func TestKeysWithoutACreatorSubjectAreLeftOut(t *testing.T) {
	om, client := newFakeOpenMeter(t)

	err := NewCustomerSync(fakeKeys{key("user-carol-a-1", "carol", "")}, client, nil).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(om.customers) != 0 {
		t.Errorf("customers = %v, want none", om.customers)
	}
}

// The sync runs on a timer; when nothing changed it must not write.
func TestRepeatedSyncWritesNothing(t *testing.T) {
	om, client := newFakeOpenMeter(t)
	s := NewCustomerSync(fakeKeys{key("user-alice-a-1", "alice", "sub-alice")}, client, nil)
	ctx := context.Background()

	if err := s.Sync(ctx); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	before := om.writes
	if err := s.Sync(ctx); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if om.writes != before {
		t.Errorf("second Sync made %d writes, want 0", om.writes-before)
	}
}

func TestOpenMeterFailureIsReported(t *testing.T) {
	om, client := newFakeOpenMeter(t)
	om.fail = true

	err := NewCustomerSync(fakeKeys{key("user-alice-a-1", "alice", "sub-alice")}, client, nil).Sync(context.Background())
	if err == nil {
		t.Error("Sync returned no error while OpenMeter was failing")
	}
}
