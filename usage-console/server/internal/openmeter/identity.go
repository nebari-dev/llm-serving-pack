package openmeter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/oidc"
)

// Identity says who is calling the console.
type Identity interface {
	// Caller returns the person making r, and how they signed in. An error
	// wrapping oidc.ErrInvalid means r has no acceptable sign-in.
	Caller(r *http.Request) (SessionActor, SessionAuth, error)
}

// DevIdentity is for a console with no identity provider configured: every
// caller is the same viewer, with nothing to sign out of.
type DevIdentity struct{}

func (DevIdentity) Caller(*http.Request) (SessionActor, SessionAuth, error) {
	return SessionActor{Email: "viewer", Roles: []string{"viewer"}}, SessionAuth{Mode: "dev"}, nil
}

// TokenVerifier checks an access token and returns its claims.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (oidc.Claims, error)
}

// GatewayIdentity is for a console behind the Nebari gateway's sign-in: the
// gateway forwards the person's Keycloak access token as
// "Authorization: Bearer", and Tokens checks it.
type GatewayIdentity struct {
	Tokens TokenVerifier
}

// gatewaySignOut is the gateway's OIDC logout path (the one the Nebari
// operator sets on every NebariApp's SecurityPolicy): the gateway ends the
// session there and sends the browser on to Keycloak's logout.
const gatewaySignOut = "/logout"

func (g GatewayIdentity) Caller(r *http.Request) (SessionActor, SessionAuth, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return SessionActor{}, SessionAuth{}, fmt.Errorf("%w: no bearer token", oidc.ErrInvalid)
	}
	c, err := g.Tokens.Verify(r.Context(), raw)
	if err != nil {
		return SessionActor{}, SessionAuth{}, err
	}
	name := c.Name
	if name == "" {
		name = strings.TrimSpace(c.GivenName + " " + c.FamilyName)
	}
	email := c.Email
	if email == "" {
		email = c.PreferredUsername
	}
	return SessionActor{Name: name, Email: email, Roles: []string{"viewer"}}, SessionAuth{Mode: "oidc", SignOutURL: gatewaySignOut}, nil
}

type callerKey struct{}

// authenticated answers 401 for a request with no acceptable sign-in, and
// 503 when sign-in cannot be checked; otherwise next sees the caller in the
// request's context.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, auth, err := s.Identity.Caller(r)
		switch {
		case errors.Is(err, oidc.ErrInvalid):
			slog.Info("sign-in refused", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthenticated", "message": "sign in again"}})
			return
		case err != nil:
			slog.Error("sign-in cannot be checked", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "identity_unavailable", "message": "sign-in cannot be checked right now"}})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, Session{Actor: actor, Auth: auth})))
	}
}

// caller is the person authenticated found for r.
func caller(r *http.Request) (SessionActor, SessionAuth) {
	s, _ := r.Context().Value(callerKey{}).(Session)
	return s.Actor, s.Auth
}
