package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/demo"
)

// Identity headers ExtAuthz puts on an admitted request. The access log
// records them and receipt-ingest turns them back into the receipt's key,
// team and project. aigw/config.yaml strips any the caller sends.
const (
	HeaderKeyID   = "X-Stargate-Key-Id"
	HeaderTeam    = "X-Stargate-Team"
	HeaderProject = "X-Stargate-Project"
	// HeaderSecretID names which of the key's secrets was used (SecretID).
	HeaderSecretID = "X-Stargate-Secret-Id"
	// HeaderModel is on a 403 only: the model the key may not call.
	HeaderModel = "X-Stargate-Model"
)

// ExtAuthz is the HTTP external authorization service behind Agent Router's
// SecurityPolicy. Envoy sends it each request's headers and body before the
// request goes anywhere. A 200 admits the request with the identity headers
// added and the caller's credentials removed; any other response goes back to
// the caller as is.
//
// It checks the key the same way Admit does, and the model against the key's
// allowlist after aliasing. Budgets and rules are Warden's (cmd/warden).
type ExtAuthz struct {
	Snap *Current
	Now  func() time.Time
}

func (a *ExtAuthz) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	snap := a.Snap.Load()
	k, rej := Authenticate(snap, r.Header.Get("Authorization"), now())
	if rej != nil {
		writeErr(w, rej.Status, rej.Code, rej.Message)
		return
	}
	w.Header().Set(HeaderKeyID, k.ID)
	w.Header().Set(HeaderTeam, k.Team)
	w.Header().Set(HeaderProject, k.Project)
	w.Header().Set(HeaderSecretID, SecretID(demo.HashSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))))
	m := requestModel(r)
	if rej := snap.CheckModel(k, m); m != "" && rej != nil {
		// A 403 goes to the caller, whose own key it is, with the identity
		// headers above; the access log reads them so the block gets a receipt.
		w.Header().Set(HeaderModel, m)
		writeErr(w, rej.Status, rej.Code, rej.Message)
		return
	}
	// The provider gets its own credentials from the backend, never the caller's.
	w.Header().Set("X-Envoy-Auth-Headers-To-Remove", "authorization,x-api-key")
	w.WriteHeader(http.StatusOK)
}

// requestModel is the model the caller asked for: the JSON body's "model".
// Requests without one (GET /v1/models) only need a valid key.
func requestModel(r *http.Request) string {
	var body struct {
		Model string `json:"model"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&body)
	return body.Model
}

// SecretID identifies one of a key's secrets on its receipts without
// revealing it: the first 12 hex of the secret's hash. It doesn't change
// when a rotation promotes the new secret.
func SecretID(hash string) string { return hash[:min(12, len(hash))] }
