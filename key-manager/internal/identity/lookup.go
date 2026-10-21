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

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// Owner is the person who minted an API key.
type Owner struct {
	ClientID string   `json:"clientId"`
	Subject  string   `json:"subject"`
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

// KeyLister lists the metadata of every API key (secrets.Manager).
type KeyLister interface {
	ListAllKeys(ctx context.Context) ([]secrets.KeyInfo, error)
}

// Handler serves the key-owner lookup.
type Handler struct {
	keys KeyLister

	mu     sync.Mutex
	owners map[string]Owner // as of the last read of the key store
}

// NewHandler returns a Handler over keys.
func NewHandler(keys KeyLister) *Handler {
	return &Handler{keys: keys}
}

// RegisterRoutes registers GET /internal/v1/key-owners/{clientID}.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /internal/v1/key-owners/{clientID}", h.getOwner)
}

// owner returns the owner of a key. Owners are remembered between calls; an
// unknown client ID triggers one fresh read of the key store, which is what
// finds a key minted since the last read.
func (h *Handler) owner(ctx context.Context, clientID string) (Owner, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if o, ok := h.owners[clientID]; ok {
		return o, true, nil
	}
	keys, err := h.keys.ListAllKeys(ctx)
	if err != nil {
		return Owner{}, false, err
	}
	h.owners = make(map[string]Owner, len(keys))
	for _, k := range keys {
		h.owners[k.ClientID] = Owner{ClientID: k.ClientID, Subject: k.CreatorSubject, Username: k.Creator, Groups: k.CreatorGroups}
	}
	o, ok := h.owners[clientID]
	return o, ok, nil
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
