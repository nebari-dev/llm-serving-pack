package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/demo"
)

// Identity headers ExtAuthz puts on an admitted request. The access log
// records them and receipt-ingest turns them back into the receipt's key,
// team and project. aigw/base.yaml strips any the caller sends.
const (
	HeaderKeyID = "X-Stargate-Key-Id"
	HeaderTeam  = "X-Stargate-Team"
	// HeaderProject is the project's name, URL-escaped (names may have
	// spaces and any letters); ProjectName reads it back.
	HeaderProject   = "X-Stargate-Project"
	HeaderProjectID = "X-Stargate-Project-Id"
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
// Anthropic-style callers (APIOf) send the key as x-api-key, get refusals in
// Anthropic's shape, and are marked with HeaderAPI for the routes.
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
	api := APIOf(r.URL.RequestURI())
	secret := callerSecret(r.Header)
	k, rej := Authenticate(snap, secret, now())
	if rej != nil {
		writeAPIErr(w, api, rej.Status, rej.Code, rej.Message)
		return
	}
	w.Header().Set(HeaderKeyID, k.ID)
	w.Header().Set(HeaderTeam, k.Team)
	w.Header().Set(HeaderProject, url.PathEscape(k.Project))
	w.Header().Set(HeaderProjectID, k.ProjectID)
	w.Header().Set(HeaderSecretID, SecretID(demo.HashSecret(strings.TrimPrefix(secret, "Bearer "))))
	m := requestModel(r)
	if rej := snap.CheckModel(k, m); m != "" && rej != nil {
		// A 403 goes to the caller, whose own key it is, with the identity
		// headers above; the access log reads them so the block gets a receipt.
		w.Header().Set(HeaderModel, m)
		writeAPIErr(w, api, rej.Status, rej.Code, rej.Message)
		return
	}
	if api == APIAnthropic {
		w.Header().Set(HeaderAPI, APIAnthropic)
	}
	// The provider gets its own credentials from the backend, never the caller's.
	w.Header().Set("X-Envoy-Auth-Headers-To-Remove", "authorization,x-api-key")
	w.WriteHeader(http.StatusOK)
}

// callerSecret is the gateway key as an Authorization value: the bearer
// token OpenAI's SDK sends, else the x-api-key Anthropic's sends.
func callerSecret(h http.Header) string {
	if a := h.Get("Authorization"); a != "" {
		return a
	}
	if k := h.Get("X-Api-Key"); k != "" {
		return "Bearer " + k
	}
	return ""
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

// ProjectName reads HeaderProject's value back into the name. A value that
// isn't valid escaping is taken as it is.
func ProjectName(v string) string {
	if n, err := url.PathUnescape(v); err == nil {
		return n
	}
	return v
}
