// Package identity answers "who owns this API key?" for the usage pipeline.
//
// The gateway's usage records name an API key only by its client ID. Usage is
// billed to people and teams, so the pipeline looks the client ID up here and
// gets back the creator's OIDC subject, username and groups as recorded when
// the key was minted.
//
// The lookup is served on its own internal listener, never through the public
// key API: it answers for any key without authentication.
package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// Owner is the person who minted an API key.
type Owner struct {
	ClientID string   `json:"clientId"`
	Subject  string   `json:"subject"`
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

// KeyLister lists the metadata of every API key, and the owners of recently
// revoked ones (secrets.Manager).
type KeyLister interface {
	ListAllKeysIncludingRevoked(ctx context.Context) ([]secrets.KeyInfo, error)
}

// minRefreshInterval is the least time between two reads of the key store.
// Each read lists every key from the Kubernetes API, and an unknown client ID
// is what triggers one.
const minRefreshInterval = time.Second

// Handler serves the key-owner lookup.
type Handler struct {
	keys KeyLister

	mu     sync.RWMutex
	owners map[string]Owner // every key seen in the key store, revoked or not

	refreshMu   sync.Mutex // held while reading the key store
	lastRefresh time.Time  // when the last read started
	minInterval time.Duration
}

// NewHandler returns a Handler over keys.
func NewHandler(keys KeyLister) *Handler {
	return &Handler{keys: keys, owners: map[string]Owner{}, minInterval: minRefreshInterval}
}

// RegisterRoutes registers GET /internal/v1/key-owners/{clientID}.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /internal/v1/key-owners/{clientID}", h.getOwner)
}

// owner returns the owner of a key.
//
// A client ID is never reissued, so its owner never changes: owners are
// remembered for good, including after the key is revoked, since the access
// log for a key's last requests can arrive after that. A revoked key's owner
// is also kept in the key store (secrets.RevokedKeyRetention), so it is
// still found after the key manager restarts. An unknown client ID
// (usually a key minted since the last read) triggers a fresh read of the key
// store. Reads are at least minInterval apart, and lookups that arrive while
// one is in progress share it, so unknown IDs cannot make the key manager
// list every key from the Kubernetes API once each.
func (h *Handler) owner(ctx context.Context, clientID string) (Owner, bool, error) {
	if o, ok := h.cached(clientID); ok {
		return o, true, nil
	}
	arrived := time.Now()
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	if h.lastRefresh.After(arrived) {
		// The key store was read after this lookup arrived.
		o, ok := h.cached(clientID)
		return o, ok, nil
	}
	if wait := time.Until(h.lastRefresh.Add(h.minInterval)); wait > 0 {
		select {
		case <-ctx.Done():
			return Owner{}, false, ctx.Err()
		case <-time.After(wait):
		}
	}
	// When the read starts, not when it ends: a key minted during the read
	// may be missing from it, so a lookup that arrived meanwhile reads again.
	h.lastRefresh = time.Now()
	keys, err := h.keys.ListAllKeysIncludingRevoked(ctx)
	if err != nil {
		return Owner{}, false, err
	}
	h.mu.Lock()
	for _, k := range keys {
		h.owners[k.ClientID] = Owner{ClientID: k.ClientID, Subject: k.CreatorSubject, Username: k.Creator, Groups: k.CreatorGroups}
	}
	h.mu.Unlock()
	o, ok := h.cached(clientID)
	return o, ok, nil
}

func (h *Handler) cached(clientID string) (Owner, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	o, ok := h.owners[clientID]
	return o, ok
}

func (h *Handler) getOwner(w http.ResponseWriter, r *http.Request) {
	o, ok, err := h.owner(r.Context(), r.PathValue("clientID"))
	switch {
	case err != nil:
		http.Error(w, "reading API keys", http.StatusServiceUnavailable)
	case !ok:
		http.Error(w, "unknown API key", http.StatusNotFound)
	default:
		if o.Groups == nil {
			o.Groups = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(o)
	}
}
