package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/models"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// discardLogger returns a slog logger that discards all output, for tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- mock implementations ---

type mockModelLister struct {
	models []models.ModelInfo
}

func (m *mockModelLister) FilterModelsForUser(groups []string) []models.ModelInfo {
	return m.models
}

type mockKeyManager struct {
	keys          []secrets.KeyInfo
	createResult  *secrets.CreateKeyResult
	createErr     error
	revokeErr     error
	listUserErr   error
}

func (m *mockKeyManager) CreateKey(ctx context.Context, modelName, username, description string) (*secrets.CreateKeyResult, error) {
	return m.createResult, m.createErr
}

func (m *mockKeyManager) ListKeys(ctx context.Context, modelName string) ([]secrets.KeyInfo, error) {
	return m.keys, nil
}

func (m *mockKeyManager) ListKeysForUser(ctx context.Context, username string) ([]secrets.KeyInfo, error) {
	if m.listUserErr != nil {
		return nil, m.listUserErr
	}
	return m.keys, nil
}

func (m *mockKeyManager) RevokeKey(ctx context.Context, modelName, clientID string) error {
	return m.revokeErr
}

// --- helpers ---

// contextWithUser returns a context that has the given user set.
func contextWithUser(ctx context.Context, user *UserInfo) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

func newHandlerWithMocks(lister ModelLister, km KeyManager) *Handler {
	return &Handler{
		lister:  lister,
		secrets: km,
		logger:  discardLogger(),
	}
}

func callHandler(t *testing.T, h *Handler, method, path string, body interface{}, user *UserInfo) *httptest.ResponseRecorder {
	t.Helper()

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if user != nil {
		req = req.WithContext(contextWithUser(req.Context(), user))
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// --- test data ---

var testUser = &UserInfo{
	Username: "chuck",
	Groups:   []string{"ml-team", "admins"},
}

var testModels = []models.ModelInfo{
	{Name: "llama3", Namespace: "default", ModelName: "meta-llama/Meta-Llama-3-8B", Public: false, Groups: []string{"ml-team"}},
	{Name: "phi3", Namespace: "default", ModelName: "microsoft/Phi-3-mini", Public: true},
}

var testKeys = []secrets.KeyInfo{
	{
		ClientID:    "user-chuck-1",
		Creator:     "chuck",
		Description: "my key",
		CreatedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ModelName:   "llama3",
		Namespace:   "default",
	},
	{
		ClientID:    "user-alice-1",
		Creator:     "alice",
		Description: "alice key",
		CreatedAt:   time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
		ModelName:   "llama3",
		Namespace:   "default",
	},
}

// --- GET /api/me tests ---

func TestGetMe(t *testing.T) {
	t.Run("returns identity for authenticated user", func(t *testing.T) {
		user := &UserInfo{
			Username: "chuck",
			Name:     "Chuck Norris",
			Email:    "chuck@example.com",
			Groups:   []string{"ml-team", "admins"},
		}
		h := newHandlerWithMocks(&mockModelLister{}, &mockKeyManager{})
		rr := callHandler(t, h, http.MethodGet, "/api/me", nil, user)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Username string   `json:"username"`
			Name     string   `json:"name"`
			Email    string   `json:"email"`
			Groups   []string `json:"groups"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if resp.Username != "chuck" || resp.Name != "Chuck Norris" || resp.Email != "chuck@example.com" {
			t.Errorf("identity mismatch: got %+v", resp)
		}
		if len(resp.Groups) != 2 {
			t.Errorf("groups = %v, want 2 entries", resp.Groups)
		}
	})

	t.Run("returns empty (non-null) groups when user has none", func(t *testing.T) {
		h := newHandlerWithMocks(&mockModelLister{}, &mockKeyManager{})
		rr := callHandler(t, h, http.MethodGet, "/api/me", nil, &UserInfo{Username: "solo"})

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
		var resp struct {
			Groups []string `json:"groups"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if resp.Groups == nil {
			t.Error("groups should serialize as [], got null")
		}
	})

	t.Run("returns 401 when no user in context", func(t *testing.T) {
		h := newHandlerWithMocks(&mockModelLister{}, &mockKeyManager{})
		rr := callHandler(t, h, http.MethodGet, "/api/me", nil, nil)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
		}
	})
}

// --- GET /api/models tests ---

func TestGetModels(t *testing.T) {
	tests := []struct {
		name           string
		user           *UserInfo
		models         []models.ModelInfo
		wantStatus     int
		wantModelCount int
	}{
		{
			name:           "returns filtered models for authenticated user",
			user:           testUser,
			models:         testModels,
			wantStatus:     http.StatusOK,
			wantModelCount: 2,
		},
		{
			name:           "returns empty list when no models accessible",
			user:           testUser,
			models:         []models.ModelInfo{},
			wantStatus:     http.StatusOK,
			wantModelCount: 0,
		},
		{
			name:       "returns 401 when no user in context",
			user:       nil,
			models:     testModels,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandlerWithMocks(
				&mockModelLister{models: tc.models},
				&mockKeyManager{},
			)
			rr := callHandler(t, h, http.MethodGet, "/api/models", nil, tc.user)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}

			var resp struct {
				Models []models.ModelInfo `json:"models"`
			}
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if len(resp.Models) != tc.wantModelCount {
				t.Errorf("model count = %d, want %d", len(resp.Models), tc.wantModelCount)
			}
		})
	}
}

// --- GET /api/keys tests ---

func TestGetKeys(t *testing.T) {
	tests := []struct {
		name       string
		user       *UserInfo
		keys       []secrets.KeyInfo
		wantStatus int
		wantCount  int
	}{
		{
			name:       "returns user keys",
			user:       testUser,
			keys:       testKeys,
			wantStatus: http.StatusOK,
			wantCount:  2,
		},
		{
			name:       "returns empty list when no keys",
			user:       testUser,
			keys:       []secrets.KeyInfo{},
			wantStatus: http.StatusOK,
			wantCount:  0,
		},
		{
			name:       "returns 401 when no user in context",
			user:       nil,
			keys:       testKeys,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandlerWithMocks(
				&mockModelLister{},
				&mockKeyManager{keys: tc.keys},
			)
			rr := callHandler(t, h, http.MethodGet, "/api/keys", nil, tc.user)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}

			var resp struct {
				Keys []secrets.KeyInfo `json:"keys"`
			}
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if len(resp.Keys) != tc.wantCount {
				t.Errorf("key count = %d, want %d", len(resp.Keys), tc.wantCount)
			}
		})
	}
}

// --- POST /api/keys tests ---

func TestCreateKey(t *testing.T) {
	type createKeyRequest struct {
		ModelName   string `json:"modelName"`
		Description string `json:"description"`
	}

	tests := []struct {
		name           string
		user           *UserInfo
		body           interface{}
		accessModels   []models.ModelInfo
		createResult   *secrets.CreateKeyResult
		createErr      error
		wantStatus     int
		wantClientID   string
		wantAPIKey     string
	}{
		{
			name: "creates key and returns 201 with apiKey",
			user: testUser,
			body: createKeyRequest{ModelName: "llama3", Description: "my key"},
			accessModels: []models.ModelInfo{
				{Name: "llama3", Namespace: "default", ModelName: "meta-llama/Meta-Llama-3-8B"},
			},
			createResult: &secrets.CreateKeyResult{
				ClientID: "user-chuck-1",
				APIKey:   "sk-abc123",
			},
			wantStatus:   http.StatusCreated,
			wantClientID: "user-chuck-1",
			wantAPIKey:   "sk-abc123",
		},
		{
			name: "returns 403 when user doesn't have model access",
			user: testUser,
			body: createKeyRequest{ModelName: "restricted-model", Description: "my key"},
			accessModels: []models.ModelInfo{
				{Name: "llama3", Namespace: "default", ModelName: "meta-llama/Meta-Llama-3-8B"},
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name:         "returns 400 for invalid JSON body",
			user:         testUser,
			body:         nil, // will send raw invalid JSON below
			accessModels: testModels,
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:       "returns 401 when no user in context",
			user:       nil,
			body:       createKeyRequest{ModelName: "llama3", Description: "my key"},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandlerWithMocks(
				&mockModelLister{models: tc.accessModels},
				&mockKeyManager{createResult: tc.createResult, createErr: tc.createErr},
			)

			var req *http.Request
			if tc.name == "returns 400 for invalid JSON body" {
				req = httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewBufferString("{invalid json"))
				req.Header.Set("Content-Type", "application/json")
			} else {
				var bodyBytes []byte
				if tc.body != nil {
					var err error
					bodyBytes, err = json.Marshal(tc.body)
					if err != nil {
						t.Fatalf("marshaling body: %v", err)
					}
				}
				req = httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(bodyBytes))
				req.Header.Set("Content-Type", "application/json")
			}

			if tc.user != nil {
				req = req.WithContext(contextWithUser(req.Context(), tc.user))
			}

			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if tc.wantStatus != http.StatusCreated {
				return
			}

			var resp struct {
				ClientID string `json:"clientId"`
				APIKey   string `json:"apiKey"`
			}
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if resp.ClientID != tc.wantClientID {
				t.Errorf("clientId = %q, want %q", resp.ClientID, tc.wantClientID)
			}
			if resp.APIKey != tc.wantAPIKey {
				t.Errorf("apiKey = %q, want %q", resp.APIKey, tc.wantAPIKey)
			}
		})
	}
}

// --- DELETE /api/keys/{namespace}/{model}/{clientID} tests ---

func TestDeleteKey(t *testing.T) {
	tests := []struct {
		name       string
		user       *UserInfo
		path       string
		keys       []secrets.KeyInfo
		revokeErr  error
		wantStatus int
	}{
		{
			name:  "returns 204 when key belongs to user",
			user:  testUser,
			path:  "/api/keys/default/llama3/user-chuck-1",
			keys:  testKeys,
			wantStatus: http.StatusNoContent,
		},
		{
			name: "returns 403 when user doesn't own the key",
			user: testUser,
			path: "/api/keys/default/llama3/user-alice-1",
			keys: testKeys,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "returns 404 when key not found",
			user:       testUser,
			path:       "/api/keys/default/llama3/user-chuck-99",
			keys:       testKeys,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "returns 401 when no user in context",
			user:       nil,
			path:       "/api/keys/default/llama3/user-chuck-1",
			keys:       testKeys,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandlerWithMocks(
				&mockModelLister{},
				&mockKeyManager{keys: tc.keys, revokeErr: tc.revokeErr},
			)
			rr := callHandler(t, h, http.MethodDelete, tc.path, nil, tc.user)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}
