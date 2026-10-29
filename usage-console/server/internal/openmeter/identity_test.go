package openmeter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/shared/oidc"
	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/openmeter"
)

// tokens stands in for the OIDC verifier: the tokens it accepts, and the
// claims each carries. Any other token is invalid.
type tokens map[string]map[string]any

func (ts tokens) ValidateToken(_ context.Context, raw string) (map[string]any, error) {
	if raw == "keycloak-down" {
		return nil, oidc.ErrNotReady
	}
	if raw == "keys-unavailable" {
		// As the validator wraps it, mid key rotation with Keycloak down.
		return nil, fmt.Errorf("token validation failed: %w", oidc.ErrKeysUnavailable)
	}
	c, ok := ts[raw]
	if !ok {
		return nil, fmt.Errorf("token validation failed: bad signature")
	}
	return c, nil
}

func signedInAPI(t *testing.T, ts tokens) http.Handler {
	t.Helper()
	om := (&fakeOpenMeter{}).server(t)
	return (&openmeter.Server{
		Client: openmeter.NewClient(om.URL, ""), Tenant: "demo", Now: func() time.Time { return now },
		Identity: openmeter.GatewayIdentity{Tokens: ts},
	}).Handler()
}

func call(h http.Handler, path, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/demo"+path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Behind the gateway, the session is the person Keycloak signed in, read
// from the access token the gateway forwards; sign-out is the gateway's.
func TestSessionIsTheSignedInPerson(t *testing.T) {
	h := signedInAPI(t, tokens{"t-alice": {"sub": "u-1", "name": "Alice Example", "preferred_username": "alice", "email": "alice@example.com"}})

	rec := call(h, "/session", "Bearer t-alice")

	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var s openmeter.Session
	json.Unmarshal(rec.Body.Bytes(), &s)
	if s.Actor.Name != "Alice Example" || s.Actor.Email != "alice@example.com" || len(s.Actor.Roles) != 1 || s.Actor.Roles[0] != "viewer" {
		t.Fatalf("actor %+v", s.Actor)
	}
	if s.Auth.Mode != "oidc" || s.Auth.SignOutURL != "/logout" {
		t.Fatalf("auth %+v", s.Auth)
	}
}

// A token without name or email still names the person: given and family
// name, and the username.
func TestSessionFallsBackToOtherNameClaims(t *testing.T) {
	h := signedInAPI(t, tokens{"t-bob": {"sub": "u-2", "given_name": "Bob", "family_name": "Example", "preferred_username": "bob"}})

	var s openmeter.Session
	json.Unmarshal(call(h, "/session", "Bearer t-bob").Body.Bytes(), &s)

	if s.Actor.Name != "Bob Example" || s.Actor.Email != "bob" {
		t.Fatalf("actor %+v", s.Actor)
	}
}

// With sign-in configured, no request is answered without a valid token:
// the console never invents a user, and its data is not readable around
// the gateway. An unreachable Keycloak is the console's problem, not the
// caller's.
func TestCallsNeedAValidToken(t *testing.T) {
	h := signedInAPI(t, tokens{"t-alice": {"sub": "u-1", "email": "alice@example.com"}})
	for _, path := range []string{"/session", "/teams", "/receipts", "/stream/traffic"} {
		for auth, want := range map[string]int{
			"":                        http.StatusUnauthorized,
			"Bearer not-a-token":      http.StatusUnauthorized,
			"Basic dDphbGljZQ==":      http.StatusUnauthorized,
			"Bearer keycloak-down":    http.StatusServiceUnavailable,
			"Bearer keys-unavailable": http.StatusServiceUnavailable,
		} {
			if rec := call(h, path, auth); rec.Code != want {
				t.Errorf("GET %s with %q: %d %s, want %d", path, auth, rec.Code, rec.Body, want)
			}
		}
	}
}

// With groups required, only their members get in, whether Keycloak writes
// group paths (/name) or names; everyone else signed in gets 403.
func TestCallsNeedARequiredGroup(t *testing.T) {
	om := (&fakeOpenMeter{}).server(t)
	h := (&openmeter.Server{
		Client: openmeter.NewClient(om.URL, ""), Tenant: "demo", Now: func() time.Time { return now },
		Identity: openmeter.GatewayIdentity{Tokens: tokens{
			"t-path":  {"sub": "u-1", "groups": []string{"/team-a", "/llm-usage-admins"}},
			"t-name":  {"sub": "u-2", "groups": []string{"llm-usage-admins"}},
			"t-other": {"sub": "u-3", "groups": []string{"/team-a", "/llm-usage-admins/sub"}},
			"t-none":  {"sub": "u-4"},
		}, Groups: []string{"llm-usage-admins"}},
	}).Handler()
	for token, want := range map[string]int{"t-path": 200, "t-name": 200, "t-other": 403, "t-none": 403} {
		if rec := call(h, "/session", "Bearer "+token); rec.Code != want {
			t.Errorf("%s: %d %s, want %d", token, rec.Code, rec.Body, want)
		}
	}
}
