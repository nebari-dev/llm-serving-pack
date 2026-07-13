package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testRealm  = "nebari"
	testIssuer = "https://keycloak.example.com"
	testKID    = "test-key-1"
)

// newTestValidator returns a JWTValidator preloaded with pub under testKID and
// marked ready, so tests validate signed tokens without any network fetch.
func newTestValidator(t *testing.T, pub *rsa.PublicKey) *JWTValidator {
	t.Helper()
	v := &JWTValidator{
		logger:      slog.Default(),
		keycloakURL: testIssuer,
		issuerURL:   testIssuer,
		realm:       testRealm,
		publicKeys:  map[string]*rsa.PublicKey{testKID: pub},
		baseCtx:     context.Background(),
	}
	v.ready.Store(true)
	return v
}

// signToken signs claims as an RS256 JWT with testKID. iss/exp are filled with
// valid defaults unless already present.
func signToken(t *testing.T, priv *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	if _, ok := claims["iss"]; !ok {
		claims["iss"] = testIssuer + "/realms/" + testRealm
	}
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(5 * time.Minute).Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testKID
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return s
}

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return key
}

// handlerThatChecksUser writes the extracted username/groups to the response.
func handlerThatChecksUser(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFromContext(r.Context())
		if !ok {
			t.Error("expected user in context, got none")
			http.Error(w, "no user", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Username", user.Username)
		groupsJSON, _ := json.Marshal(user.Groups)
		w.Header().Set("X-Groups", string(groupsJSON))
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthMiddleware(t *testing.T) {
	priv := genKey(t)
	otherPriv := genKey(t)
	validator := newTestValidator(t, &priv.PublicKey)

	tests := []struct {
		name         string
		cfg          AuthConfig
		setupRequest func(r *http.Request)
		wantStatus   int
		wantUsername string
		wantGroups   []string
	}{
		{
			name: "valid bearer token extracts username and groups",
			cfg:  AuthConfig{GroupsClaim: "groups", Validator: validator},
			setupRequest: func(r *http.Request) {
				token := signToken(t, priv, jwt.MapClaims{
					"preferred_username": "alice",
					"groups":             []string{"admins", "users"},
				})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus:   http.StatusOK,
			wantUsername: "alice",
			wantGroups:   []string{"admins", "users"},
		},
		{
			name:         "no token returns 401",
			cfg:          AuthConfig{GroupsClaim: "groups", Validator: validator},
			setupRequest: func(r *http.Request) {},
			wantStatus:   http.StatusUnauthorized,
		},
		{
			name: "Authorization header without Bearer prefix returns 401",
			cfg:  AuthConfig{GroupsClaim: "groups", Validator: validator},
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "some-token")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "garbage token returns 401",
			cfg:  AuthConfig{GroupsClaim: "groups", Validator: validator},
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer notajwt")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "token signed by an unknown key returns 401",
			cfg:  AuthConfig{GroupsClaim: "groups", Validator: validator},
			setupRequest: func(r *http.Request) {
				token := signToken(t, otherPriv, jwt.MapClaims{"preferred_username": "mallory"})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "token with wrong issuer returns 401",
			cfg:  AuthConfig{GroupsClaim: "groups", Validator: validator},
			setupRequest: func(r *http.Request) {
				token := signToken(t, priv, jwt.MapClaims{
					"preferred_username": "alice",
					"iss":                "https://evil.example.com/realms/nebari",
				})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired token returns 401",
			cfg:  AuthConfig{GroupsClaim: "groups", Validator: validator},
			setupRequest: func(r *http.Request) {
				token := signToken(t, priv, jwt.MapClaims{
					"preferred_username": "alice",
					"exp":                time.Now().Add(-time.Hour).Unix(),
				})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "custom groups claim name is respected",
			cfg:  AuthConfig{GroupsClaim: "cognito:groups", Validator: validator},
			setupRequest: func(r *http.Request) {
				token := signToken(t, priv, jwt.MapClaims{
					"preferred_username": "henry",
					"cognito:groups":     []string{"cognito-admins"},
				})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus:   http.StatusOK,
			wantUsername: "henry",
			wantGroups:   []string{"cognito-admins"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			middleware := AuthMiddleware(tc.cfg)
			var handler http.Handler
			if tc.wantStatus == http.StatusOK {
				handler = middleware(handlerThatChecksUser(t))
			} else {
				handler = middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("inner handler should not be called when auth fails")
					w.WriteHeader(http.StatusOK)
				}))
			}

			req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
			tc.setupRequest(req)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}

			if tc.wantStatus == http.StatusOK {
				if got := rec.Header().Get("X-Username"); got != tc.wantUsername {
					t.Errorf("username: got %q, want %q", got, tc.wantUsername)
				}
				var gotGroups []string
				if err := json.Unmarshal([]byte(rec.Header().Get("X-Groups")), &gotGroups); err != nil {
					t.Fatalf("failed to parse groups header %q: %v", rec.Header().Get("X-Groups"), err)
				}
				if len(gotGroups) != len(tc.wantGroups) {
					t.Fatalf("groups: got %v, want %v", gotGroups, tc.wantGroups)
				}
				for i := range gotGroups {
					if gotGroups[i] != tc.wantGroups[i] {
						t.Errorf("groups[%d]: got %q, want %q", i, gotGroups[i], tc.wantGroups[i])
					}
				}
			}
		})
	}
}

// TestAuthMiddlewareNotReady checks that requests during JWKS warmup get a 503
// with Retry-After, not a 401, so clients can distinguish "auth not online yet".
func TestAuthMiddlewareNotReady(t *testing.T) {
	priv := genKey(t)
	v := newTestValidator(t, &priv.PublicKey)
	v.ready.Store(false)

	handler := AuthMiddleware(AuthConfig{Validator: v})(handlerThatChecksUser(t))
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, priv, jwt.MapClaims{"preferred_username": "alice"}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 503")
	}
}

// TestAuthMiddlewareMisconfigured checks that a non-dev config without a
// validator fails closed with 500 rather than admitting the request.
func TestAuthMiddlewareMisconfigured(t *testing.T) {
	handler := AuthMiddleware(AuthConfig{})(handlerThatChecksUser(t))
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// fakeValidator is a TokenValidator stand-in: it records the context and token it
// was handed and returns canned claims, so middleware behaviour can be tested
// without a real JWKS or Keycloak.
type fakeValidator struct {
	claims map[string]interface{}
	err    error
	gotCtx context.Context
	gotTok string
}

func (f *fakeValidator) ValidateToken(ctx context.Context, token string) (map[string]interface{}, error) {
	f.gotCtx = ctx
	f.gotTok = token
	return f.claims, f.err
}

// TestAuthMiddlewareWithFakeValidator proves AuthConfig.Validator is satisfied by
// any TokenValidator: the middleware passes the request context and bearer token
// through and maps the returned claims onto the request identity, with no
// *JWTValidator or Keycloak involved.
func TestAuthMiddlewareWithFakeValidator(t *testing.T) {
	fake := &fakeValidator{claims: map[string]interface{}{
		"preferred_username": "fakeuser",
		"groups":             []interface{}{"llm"},
	}}
	handler := AuthMiddleware(AuthConfig{Validator: fake})(handlerThatChecksUser(t))
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	req.Header.Set("Authorization", "Bearer abc.def.ghi")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Username"); got != "fakeuser" {
		t.Errorf("username: got %q, want %q", got, "fakeuser")
	}
	if fake.gotTok != "abc.def.ghi" {
		t.Errorf("validator received token %q, want the bearer value", fake.gotTok)
	}
	if fake.gotCtx == nil {
		t.Error("validator received a nil context; middleware should pass the request context through")
	}
}

func TestAuthMiddlewareDevMode(t *testing.T) {
	devIdentity := UserInfo{
		Username: "dev",
		Name:     "Dev User",
		Email:    "dev@local",
		Groups:   []string{"llm"},
	}

	tests := []struct {
		name         string
		setupRequest func(r *http.Request)
		wantUsername string
		wantGroups   []string
	}{
		{
			name:         "no token injects the dev identity instead of returning 401",
			setupRequest: func(r *http.Request) {},
			wantUsername: "dev",
			wantGroups:   []string{"llm"},
		},
		{
			name: "a supplied bearer token is ignored in favor of the dev identity",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer some-token")
			},
			wantUsername: "dev",
			wantGroups:   []string{"llm"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AuthConfig{
				GroupsClaim: "groups",
				DevMode:     true,
				DevIdentity: devIdentity,
			}
			handler := AuthMiddleware(cfg)(handlerThatChecksUser(t))

			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			tc.setupRequest(req)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("X-Username"); got != tc.wantUsername {
				t.Errorf("username: got %q, want %q", got, tc.wantUsername)
			}
			var gotGroups []string
			if err := json.Unmarshal([]byte(rec.Header().Get("X-Groups")), &gotGroups); err != nil {
				t.Fatalf("failed to parse groups header: %v", err)
			}
			if len(gotGroups) != len(tc.wantGroups) {
				t.Fatalf("groups: got %v, want %v", gotGroups, tc.wantGroups)
			}
			for i := range gotGroups {
				if gotGroups[i] != tc.wantGroups[i] {
					t.Errorf("groups[%d]: got %q, want %q", i, gotGroups[i], tc.wantGroups[i])
				}
			}
		})
	}
}

func TestUserInfoFromClaims(t *testing.T) {
	t.Run("extracts name, email, username, groups", func(t *testing.T) {
		u := userInfoFromClaims(map[string]interface{}{
			"preferred_username": "chuck",
			"name":               "Chuck Norris",
			"email":              "chuck@example.com",
			"groups":             []interface{}{"llm"},
		}, "groups")
		if u.Username != "chuck" {
			t.Errorf("username: got %q, want %q", u.Username, "chuck")
		}
		if u.Name != "Chuck Norris" {
			t.Errorf("name: got %q, want %q", u.Name, "Chuck Norris")
		}
		if u.Email != "chuck@example.com" {
			t.Errorf("email: got %q, want %q", u.Email, "chuck@example.com")
		}
		if len(u.Groups) != 1 || u.Groups[0] != "llm" {
			t.Errorf("groups: got %v, want [llm]", u.Groups)
		}
	})

	t.Run("falls back to sub when preferred_username absent", func(t *testing.T) {
		u := userInfoFromClaims(map[string]interface{}{"sub": "abc-123"}, "groups")
		if u.Username != "abc-123" {
			t.Errorf("username: got %q, want %q", u.Username, "abc-123")
		}
		if u.Name != "" || u.Email != "" {
			t.Errorf("expected empty name/email, got %q/%q", u.Name, u.Email)
		}
		if len(u.Groups) != 0 {
			t.Errorf("expected empty groups, got %v", u.Groups)
		}
	})
}

func TestExtractGroups(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]interface{}
		claim  string
		want   []string
	}{
		{"interface array", map[string]interface{}{"groups": []interface{}{"a", "b"}}, "groups", []string{"a", "b"}},
		{"single string", map[string]interface{}{"groups": "solo"}, "groups", []string{"solo"}},
		{"missing", map[string]interface{}{}, "groups", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractGroups(tc.claims, tc.claim)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestUserFromContextReturnsFalseWhenNoUserSet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	user, ok := UserFromContext(req.Context())
	if ok {
		t.Errorf("expected ok=false when no user set, got ok=true with user=%v", user)
	}
	if user != nil {
		t.Errorf("expected nil user when not set, got %v", user)
	}
}
