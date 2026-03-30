package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeTestJWT creates a test JWT with the given claims by base64url-encoding a JSON payload.
// No actual signing - Envoy Gateway handles verification in production.
func makeTestJWT(claims map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	return header + "." + payloadB64 + "." + sig
}

func defaultConfig() AuthConfig {
	return AuthConfig{
		GroupsClaim:  "groups",
		CookiePrefix: "IdToken",
	}
}

// handlerThatChecksUser is a test handler that writes the extracted username/groups to the response.
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
	tests := []struct {
		name           string
		cfg            AuthConfig
		setupRequest   func(r *http.Request)
		wantStatus     int
		wantUsername   string
		wantGroups     []string
	}{
		{
			name: "bearer JWT in Authorization header extracts username and groups",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
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
			name: "JWT in IdToken cookie extracts username and groups",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
					"preferred_username": "bob",
					"groups":             []string{"users"},
				})
				r.AddCookie(&http.Cookie{Name: "IdToken-somedeployment", Value: token})
			},
			wantStatus:   http.StatusOK,
			wantUsername: "bob",
			wantGroups:   []string{"users"},
		},
		{
			name: "no token returns 401",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				// no cookie, no Authorization header
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "invalid JWT without 3 parts returns 401",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer notajwt")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "malformed base64 in payload returns 401",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				// header.invalid-base64!.sig
				r.Header.Set("Authorization", "Bearer aGVhZGVy.!!!notbase64!!!.c2ln")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "groups claim as string array works",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
					"preferred_username": "carol",
					"groups":             []string{"eng", "ml"},
				})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus:   http.StatusOK,
			wantUsername: "carol",
			wantGroups:   []string{"eng", "ml"},
		},
		{
			name: "groups claim as single string is converted to []string",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
					"preferred_username": "dave",
					"groups":             "solo-group",
				})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus:   http.StatusOK,
			wantUsername: "dave",
			wantGroups:   []string{"solo-group"},
		},
		{
			name: "missing groups claim results in empty groups",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
					"preferred_username": "eve",
				})
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantStatus:   http.StatusOK,
			wantUsername: "eve",
			wantGroups:   []string{},
		},
		{
			name: "Authorization header without Bearer prefix returns 401",
			cfg:  defaultConfig(),
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
					"preferred_username": "frank",
				})
				r.Header.Set("Authorization", token)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "custom cookie prefix is respected",
			cfg: AuthConfig{
				GroupsClaim:  "groups",
				CookiePrefix: "OauthToken",
			},
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
					"preferred_username": "grace",
					"groups":             []string{"devs"},
				})
				r.AddCookie(&http.Cookie{Name: "OauthToken-prod", Value: token})
			},
			wantStatus:   http.StatusOK,
			wantUsername: "grace",
			wantGroups:   []string{"devs"},
		},
		{
			name: "custom groups claim name is respected",
			cfg: AuthConfig{
				GroupsClaim:  "cognito:groups",
				CookiePrefix: "IdToken",
			},
			setupRequest: func(r *http.Request) {
				token := makeTestJWT(map[string]interface{}{
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
				// For error cases, inner handler should not be called.
				handler = middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("inner handler should not be called when auth fails")
					w.WriteHeader(http.StatusOK)
				}))
			}

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			tc.setupRequest(req)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}

			if tc.wantStatus == http.StatusOK {
				gotUsername := rec.Header().Get("X-Username")
				if gotUsername != tc.wantUsername {
					t.Errorf("username: got %q, want %q", gotUsername, tc.wantUsername)
				}

				var gotGroups []string
				groupsJSON := rec.Header().Get("X-Groups")
				if err := json.Unmarshal([]byte(groupsJSON), &gotGroups); err != nil {
					t.Fatalf("failed to parse groups header %q: %v", groupsJSON, err)
				}
				if len(gotGroups) != len(tc.wantGroups) {
					t.Errorf("groups length: got %d, want %d (got %v, want %v)",
						len(gotGroups), len(tc.wantGroups), gotGroups, tc.wantGroups)
				} else {
					for i := range gotGroups {
						if gotGroups[i] != tc.wantGroups[i] {
							t.Errorf("groups[%d]: got %q, want %q", i, gotGroups[i], tc.wantGroups[i])
						}
					}
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
