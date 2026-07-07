package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// UserInfo holds the authenticated user's identity extracted from JWT claims.
type UserInfo struct {
	Username string
	Name     string
	Email    string
	Groups   []string
}

type contextKey string

const userContextKey contextKey = "user"

// UserFromContext extracts the UserInfo from the request context.
// Returns nil, false if no user has been set.
func UserFromContext(ctx context.Context) (*UserInfo, bool) {
	user, ok := ctx.Value(userContextKey).(*UserInfo)
	return user, ok && user != nil
}

// AuthConfig configures the auth middleware.
type AuthConfig struct {
	// GroupsClaim is the JWT claim containing groups (default: "groups").
	GroupsClaim string
	// Validator verifies bearer tokens against Keycloak's JWKS (signature,
	// expiry, issuer). Required unless DevMode is true.
	Validator *JWTValidator
	// DevMode disables token handling and injects DevIdentity into every
	// request. It exists so the UI can run on a local cluster that has no
	// Keycloak in front of it. It is off by default and must never be enabled
	// in a real deployment.
	DevMode bool
	// DevIdentity is the user injected into the request context when DevMode
	// is on. Ignored when DevMode is false.
	DevIdentity UserInfo
}

// AuthMiddleware returns HTTP middleware that authenticates requests using a
// Keycloak bearer token (Model B: SPA-managed Keycloak). It extracts the token
// from the Authorization header, verifies it against the realm's JWKS via
// cfg.Validator, and injects the resulting identity into the request context.
func AuthMiddleware(cfg AuthConfig) func(http.Handler) http.Handler {
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Dev mode: skip token handling entirely and inject a fixed identity
			// so the UI works without an OIDC provider in front.
			if cfg.DevMode {
				devUser := cfg.DevIdentity
				ctx := context.WithValue(r.Context(), userContextKey, &devUser)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if cfg.Validator == nil {
				http.Error(w, "auth not configured", http.StatusInternalServerError)
				return
			}

			token, err := extractBearerToken(r)
			if err != nil {
				http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
				return
			}

			claims, err := cfg.Validator.ValidateToken(token)
			if err != nil {
				// JWKS not fetched yet: transient, tell the client to retry.
				if errors.Is(err, ErrNotReady) {
					w.Header().Set("Retry-After", "5")
					http.Error(w, "auth not ready: "+err.Error(), http.StatusServiceUnavailable)
					return
				}
				http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
				return
			}

			user := userInfoFromClaims(claims, cfg.GroupsClaim)
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearerToken returns the raw JWT from the Authorization: Bearer header.
func extractBearerToken(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", fmt.Errorf("no Authorization header")
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", fmt.Errorf("authorization header must use Bearer scheme")
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		return "", fmt.Errorf("bearer token is empty")
	}
	return token, nil
}

// userInfoFromClaims builds a UserInfo from a set of verified JWT claims.
func userInfoFromClaims(claims map[string]interface{}, groupsClaim string) *UserInfo {
	username, _ := claims["preferred_username"].(string)
	if username == "" {
		// Fall back to the subject when preferred_username is absent.
		username, _ = claims["sub"].(string)
	}
	name, _ := claims["name"].(string)
	email, _ := claims["email"].(string)

	return &UserInfo{
		Username: username,
		Name:     name,
		Email:    email,
		Groups:   extractGroups(claims, groupsClaim),
	}
}

// extractGroups reads the groups claim from JWT claims.
// Supports both []string (array) and string (single value) formats.
// Returns an empty slice when the claim is absent.
func extractGroups(claims map[string]interface{}, claimName string) []string {
	raw, ok := claims[claimName]
	if !ok {
		return []string{}
	}

	switch v := raw.(type) {
	case []interface{}:
		groups := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				groups = append(groups, s)
			}
		}
		return groups
	case []string:
		return v
	case string:
		return []string{v}
	default:
		return []string{}
	}
}
