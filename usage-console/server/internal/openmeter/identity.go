package openmeter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nebari-dev/nebari-llm-serving-pack/shared/oidc"
)

// Identity says who is calling the console.
type Identity interface {
	// Caller returns the person making r, and how they signed in. An error
	// wrapping oidc.ErrNotReady or oidc.ErrKeysUnavailable means sign-in
	// cannot be checked right now; any other, that r has no acceptable
	// sign-in.
	Caller(r *http.Request) (SessionActor, SessionAuth, error)
}

// DevIdentity is for a console with no identity provider configured: every
// caller is the same viewer, with nothing to sign out of.
type DevIdentity struct{}

func (DevIdentity) Caller(*http.Request) (SessionActor, SessionAuth, error) {
	return SessionActor{Email: "viewer", Roles: []string{"viewer"}}, SessionAuth{Mode: "dev"}, nil
}

// TokenVerifier checks an access token and returns its claims
// (*oidc.Validator).
type TokenVerifier interface {
	ValidateToken(ctx context.Context, raw string) (map[string]any, error)
}

// GatewayIdentity is for a console behind the Nebari gateway's sign-in: the
// gateway forwards the person's Keycloak access token as
// "Authorization: Bearer", and Tokens checks it.
type GatewayIdentity struct {
	Tokens TokenVerifier
	// Groups, when set, are the Keycloak groups whose members may use the
	// console (any one of them). The gateway checks them too; this keeps the
	// gate if a route reaches the console without that check.
	Groups []string
}

// errForbidden is a signed-in caller outside the required groups.
var errForbidden = errors.New("not in a group allowed to use the console")

// gatewaySignOut is the gateway's OIDC logout path (the one the Nebari
// operator sets on every NebariApp's SecurityPolicy): the gateway ends the
// session there and sends the browser on to Keycloak's logout.
const gatewaySignOut = "/logout"

func (g GatewayIdentity) Caller(r *http.Request) (SessionActor, SessionAuth, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return SessionActor{}, SessionAuth{}, errors.New("no bearer token")
	}
	c, err := g.Tokens.ValidateToken(r.Context(), raw)
	if err != nil {
		return SessionActor{}, SessionAuth{}, err
	}
	name := claim(c, "name")
	if name == "" {
		name = strings.TrimSpace(claim(c, "given_name") + " " + claim(c, "family_name"))
	}
	if len(g.Groups) > 0 && !memberOfAny(groupsOf(c), g.Groups) {
		return SessionActor{}, SessionAuth{}, errForbidden
	}
	email := claim(c, "email")
	if email == "" {
		email = claim(c, "preferred_username")
	}
	return SessionActor{Name: name, Email: email, Roles: []string{"viewer"}}, SessionAuth{Mode: "oidc", SignOutURL: gatewaySignOut}, nil
}

func claim(c map[string]any, name string) string {
	s, _ := c[name].(string)
	return s
}

// groupsOf is the token's groups claim, as the operator's mapper writes it.
func groupsOf(c map[string]any) []string {
	var out []string
	switch gs := c["groups"].(type) {
	case []string:
		out = gs
	case []any:
		for _, g := range gs {
			if s, ok := g.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// memberOfAny says whether have holds one of want, comparing them as
// Keycloak group paths: a leading "/" is added and empty segments dropped,
// as the Nebari operator normalizes the groups it enforces.
func memberOfAny(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if p := groupPath(h); p != "" && p == groupPath(w) {
				return true
			}
		}
	}
	return false
}

func groupPath(g string) string {
	var segs []string
	for _, s := range strings.Split(strings.TrimSpace(g), "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return ""
	}
	return "/" + strings.Join(segs, "/")
}

type callerKey struct{}

// authenticated answers 401 for a request with no acceptable sign-in, 403
// for a caller outside the required groups, and 503 when sign-in cannot be
// checked; otherwise next sees the caller in the
// request's context.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, auth, err := s.Identity.Caller(r)
		switch {
		case err == nil:
		case errors.Is(err, errForbidden):
			slog.Info("access refused", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "forbidden", "message": "ask an administrator to add you to the group that may see usage"}})
			return
		case errors.Is(err, oidc.ErrNotReady), errors.Is(err, oidc.ErrKeysUnavailable):
			slog.Error("sign-in cannot be checked", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "identity_unavailable", "message": "sign-in cannot be checked right now"}})
			return
		default:
			slog.Info("sign-in refused", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthenticated", "message": "sign in again"}})
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
