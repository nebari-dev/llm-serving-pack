package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// fakeOpenMeter serves the slice of OpenMeter's v1 customer API the sync uses:
// GET, POST and PUT on /api/v1/customers, keyed by customer key. Customers are
// stored as the JSON objects written, so a field a write leaves out is gone,
// as with OpenMeter's PUT.
type fakeOpenMeter struct {
	mu        sync.Mutex
	customers map[string]map[string]any // by key
	writes    int                       // POST + PUT requests served
	fail      bool                      // answer every request with 500
}

func newFakeOpenMeter(t *testing.T) (*fakeOpenMeter, *Client) {
	t.Helper()
	f := &fakeOpenMeter{customers: map[string]map[string]any{}}
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
	case r.Method == http.MethodGet && key == "":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		f.list(w, r.URL.Query().Get("subject"), max(page, 1))
	case r.Method == http.MethodPost && key == "":
		var c map[string]any
		_ = json.NewDecoder(r.Body).Decode(&c)
		k, _ := c["key"].(string)
		if f.refuseTakenSubject(w, k, c) {
			return
		}
		f.customers[k] = c
		f.writes++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(c)
	case r.Method == http.MethodPut && key != "":
		var c map[string]any
		_ = json.NewDecoder(r.Body).Decode(&c)
		if f.refuseTakenSubject(w, key, c) {
			return
		}
		f.customers[key] = c
		f.writes++
		_ = json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
	}
}

// refuseTakenSubject answers 409, as OpenMeter refuses the write, when c
// names a subject key another customer already holds.
func (f *fakeOpenMeter) refuseTakenSubject(w http.ResponseWriter, key string, c map[string]any) bool {
	for _, sk := range subjectKeysOf(c) {
		for other, oc := range f.customers {
			if other != key && slices.Contains(subjectKeysOf(oc), sk) {
				http.Error(w, "subject "+sk+" belongs to customer "+other, http.StatusConflict)
				return true
			}
		}
	}
	return false
}

// list serves GET /api/v1/customers?subject=...&page=..., a case-insensitive
// partial match on subject keys, in pages of 1 so the sync must follow pages.
func (f *fakeOpenMeter) list(w http.ResponseWriter, subject string, page int) {
	var matches []map[string]any
	for _, k := range slices.Sorted(maps.Keys(f.customers)) {
		c := f.customers[k]
		if slices.ContainsFunc(subjectKeysOf(c), func(sk string) bool {
			return strings.Contains(strings.ToLower(sk), strings.ToLower(subject))
		}) {
			matches = append(matches, c)
		}
	}
	items := []map[string]any{}
	if page <= len(matches) {
		items = matches[page-1 : page]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"totalCount": len(matches), "page": page, "pageSize": 1, "items": items})
}

func subjectKeysOf(c map[string]any) []string {
	attribution, _ := c["usageAttribution"].(map[string]any)
	raw, _ := attribution["subjectKeys"].([]any)
	keys := make([]string, 0, len(raw))
	for _, k := range raw {
		keys = append(keys, k.(string))
	}
	return keys
}

// put stores a customer as given, as if created by whoever runs OpenMeter.
func (f *fakeOpenMeter) put(t *testing.T, key, customerJSON string) {
	t.Helper()
	var c map[string]any
	if err := json.Unmarshal([]byte(customerJSON), &c); err != nil {
		t.Fatalf("customer JSON: %v", err)
	}
	f.customers[key] = c
}

func (f *fakeOpenMeter) subjectKeys(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := subjectKeysOf(f.customers[key])
	slices.Sort(keys)
	return keys
}

func (f *fakeOpenMeter) field(key, name string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.customers[key][name]
}

type fakeKeys []secrets.KeyInfo

func (k fakeKeys) ListAllKeysIncludingRevoked(context.Context) ([]secrets.KeyInfo, error) { return k, nil }

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
	if name := om.field("sub-alice", "name"); name != "alice" {
		t.Errorf("alice's customer name = %q, want alice", name)
	}
}

// A customer may already exist, created by whoever runs OpenMeter, with
// subject keys the pack does not know about. They must survive the sync.
func TestExistingCustomerKeepsItsOtherSubjectKeys(t *testing.T) {
	om, client := newFakeOpenMeter(t)
	om.put(t, "sub-alice", `{"id": "01CUSTOMER", "key": "sub-alice", "name": "Alice (billing name)",
		"usageAttribution": {"subjectKeys": ["sub-alice", "batch-job-7"]}}`)

	err := NewCustomerSync(fakeKeys{key("user-alice-a-1", "alice", "sub-alice")}, client, nil).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := om.subjectKeys("sub-alice"), []string{"batch-job-7", "sub-alice", "user-alice-a-1"}; !slices.Equal(got, want) {
		t.Errorf("subject keys = %v, want %v", got, want)
	}
	if name := om.field("sub-alice", "name"); name != "Alice (billing name)" {
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

// OpenMeter's PUT replaces the whole customer. Whoever runs OpenMeter may have
// set billing details on it; adding a key's client ID must keep them.
func TestAddingAKeyKeepsTheCustomersOtherFields(t *testing.T) {
	om, client := newFakeOpenMeter(t)
	om.put(t, "sub-alice", `{"id": "01CUSTOMER", "key": "sub-alice", "name": "Alice",
		"description": "Research group lead", "primaryEmail": "alice@example.com", "currency": "USD",
		"metadata": {"costCenter": "cc-1"}, "billingAddress": {"country": "US", "city": "Springfield"},
		"usageAttribution": {"subjectKeys": ["sub-alice"]}}`)

	err := NewCustomerSync(fakeKeys{key("user-alice-a-1", "alice", "sub-alice")}, client, nil).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := om.subjectKeys("sub-alice"), []string{"sub-alice", "user-alice-a-1"}; !slices.Equal(got, want) {
		t.Errorf("subject keys = %v, want %v", got, want)
	}
	for name, want := range map[string]any{
		"description":    "Research group lead",
		"primaryEmail":   "alice@example.com",
		"currency":       "USD",
		"metadata":       map[string]any{"costCenter": "cc-1"},
		"billingAddress": map[string]any{"country": "US", "city": "Springfield"},
	} {
		if got := om.field("sub-alice", name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v (kept from before the sync)", name, got, want)
		}
	}
}

// OpenMeter lets one subject key belong to one customer only. A key's client
// ID can already be on another customer: client IDs used to be reissued
// after a revoke, and whoever runs OpenMeter can attach any subject key. The
// sync must still give the key's creator a customer with the rest of their
// keys, leave the other customer as it is, and say which client ID it could
// not attach and where it is.
func TestClientIDOnAnotherCustomerIsReportedAndTheRestAttached(t *testing.T) {
	om, client := newFakeOpenMeter(t)
	om.put(t, "sub-old-alice", `{"key": "sub-old-alice", "name": "alice",
		"usageAttribution": {"subjectKeys": ["sub-old-alice", "user-alice-a-1"]}}`)
	om.put(t, "sub-carol", `{"key": "sub-carol", "name": "carol",
		"usageAttribution": {"subjectKeys": ["sub-carol", "user-alice-a-10"]}}`)
	var logs bytes.Buffer
	s := NewCustomerSync(fakeKeys{
		key("user-alice-a-1", "alice", "sub-new-alice"),
		key("user-alice-a-2", "alice", "sub-new-alice"),
	}, client, slog.New(slog.NewTextHandler(&logs, nil)))

	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := om.subjectKeys("sub-new-alice"), []string{"sub-new-alice", "user-alice-a-2"}; !slices.Equal(got, want) {
		t.Errorf("new alice's subject keys = %v, want %v", got, want)
	}
	if got, want := om.subjectKeys("sub-old-alice"), []string{"sub-old-alice", "user-alice-a-1"}; !slices.Equal(got, want) {
		t.Errorf("old alice's subject keys = %v, want %v (left as they were)", got, want)
	}
	if !strings.Contains(logs.String(), "user-alice-a-1") || !strings.Contains(logs.String(), "sub-old-alice") {
		t.Errorf("log does not name the client ID and the customer holding it:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "sub-carol") {
		t.Errorf("log names sub-carol, whose user-alice-a-10 only partly matches user-alice-a-1:\n%s", logs.String())
	}

	logs.Reset()
	before := om.writes
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if om.writes != before {
		t.Errorf("second Sync made %d writes, want 0", om.writes-before)
	}
	if logs.Len() != 0 {
		t.Errorf("second Sync logged again for an unchanged conflict:\n%s", logs.String())
	}
}
