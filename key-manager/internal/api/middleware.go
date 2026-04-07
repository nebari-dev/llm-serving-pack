package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// UserInfo holds the authenticated user's identity extracted from JWT claims.
type UserInfo struct {
	Username string
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
	// CookiePrefix is the prefix for the IdToken cookie (default: "IdToken").
	// The middleware accepts any cookie whose name starts with this prefix.
	CookiePrefix string
}

// AuthMiddleware returns HTTP middleware that extracts user identity from JWT.
// It does NOT validate signatures - Envoy Gateway handles that upstream.
// Token sources (in priority order):
//  1. Authorization: Bearer <token> header
//  2. Cookie with name matching CookiePrefix
func AuthMiddleware(cfg AuthConfig) func(http.Handler) http.Handler {
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if cfg.CookiePrefix == "" {
		cfg.CookiePrefix = "IdToken"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := extractToken(r, cfg.CookiePrefix)
			if err != nil {
				http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
				return
			}

			user, err := parseJWT(token, cfg.GroupsClaim)
			if err != nil {
				http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken retrieves the raw JWT string from the request.
// Checks Authorization: Bearer header first, then cookies matching the prefix.
func extractToken(r *http.Request, cookiePrefix string) (string, error) {
	// Check Authorization header first.
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if !strings.HasPrefix(authHeader, "Bearer ") {
			return "", fmt.Errorf("authorization header must use Bearer scheme")
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" {
			return "", fmt.Errorf("bearer token is empty")
		}
		return token, nil
	}

	// Fall back to cookie matching the prefix.
	for _, cookie := range r.Cookies() {
		if strings.HasPrefix(cookie.Name, cookiePrefix) {
			return cookie.Value, nil
		}
	}

	return "", fmt.Errorf("no token found in Authorization header or cookie")
}

// parseJWT decodes a JWT payload (without signature verification) and extracts user identity.
func parseJWT(token, groupsClaim string) (*UserInfo, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid JWT payload encoding: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("invalid JWT payload JSON: %w", err)
	}

	username, _ := claims["preferred_username"].(string)
	groups := extractGroups(claims, groupsClaim)

	return &UserInfo{
		Username: username,
		Groups:   groups,
	}, nil
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
	case string:
		return []string{v}
	default:
		return []string{}
	}
}
