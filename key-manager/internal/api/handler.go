package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/nebari-dev/llm-serving-pack/key-manager/internal/models"
	"github.com/nebari-dev/llm-serving-pack/key-manager/internal/secrets"
)

// ModelLister provides model listing for the handler.
type ModelLister interface {
	FilterModelsForUser(groups []string) []models.ModelInfo
}

// KeyManager provides key CRUD for the handler.
type KeyManager interface {
	CreateKey(ctx context.Context, modelName, username, description string) (*secrets.CreateKeyResult, error)
	ListKeys(ctx context.Context, modelName string) ([]secrets.KeyInfo, error)
	ListKeysForUser(ctx context.Context, username string) ([]secrets.KeyInfo, error)
	RevokeKey(ctx context.Context, modelName, clientID string) error
}

// Handler provides HTTP handlers for the key manager API.
type Handler struct {
	lister  ModelLister
	secrets KeyManager
	logger  *slog.Logger
}

// NewHandler creates a Handler with the given model lister and key manager.
// If logger is nil, slog.Default() is used.
func NewHandler(w ModelLister, s KeyManager, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		lister:  w,
		secrets: s,
		logger:  logger,
	}
}

// RegisterRoutes registers all API routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// /healthz is intentionally outside /api/ so the auth middleware (which only
	// wraps /api/ in main.go) does not gate it — kubelet probes carry no bearer.
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /api/me", h.getMe)
	mux.HandleFunc("GET /api/models", h.getModels)
	mux.HandleFunc("GET /api/keys", h.getKeys)
	mux.HandleFunc("POST /api/keys", h.createKey)
	mux.HandleFunc("DELETE /api/keys/{namespace}/{model}/{clientID}", h.deleteKey)
}

// healthz is an unauthenticated liveness/readiness probe. It reports only that
// the process is up and serving HTTP; it deliberately does not gate on the JWKS
// validator being ready. Per-request readiness is surfaced by the auth
// middleware as 503, and gating the probe on it would CrashLoop the pod whenever
// Keycloak is briefly unreachable at startup — the opposite of what we want.
func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// writeJSON encodes v as JSON into w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// getMe handles GET /api/me, returning the authenticated user's identity.
func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	groups := user.Groups
	if groups == nil {
		groups = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"username": user.Username,
		"name":     user.Name,
		"email":    user.Email,
		"groups":   groups,
	})
}

// getModels handles GET /api/models.
func (h *Handler) getModels(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	accessible := h.lister.FilterModelsForUser(user.Groups)
	if accessible == nil {
		accessible = []models.ModelInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"models": accessible,
	})
}

// getKeys handles GET /api/keys.
func (h *Handler) getKeys(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	keys, err := h.secrets.ListKeysForUser(r.Context(), user.Username)
	if err != nil {
		h.logger.Error("getKeys failed", "error", err, "user", user.Username)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if keys == nil {
		keys = []secrets.KeyInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"keys": keys,
	})
}

// createKeyRequest is the expected JSON body for POST /api/keys.
type createKeyRequest struct {
	ModelName   string `json:"modelName"`
	Description string `json:"description"`
}

// createKey handles POST /api/keys.
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: invalid JSON body", http.StatusBadRequest)
		return
	}

	// Verify the user has access to the requested model.
	accessible := h.lister.FilterModelsForUser(user.Groups)
	hasAccess := false
	for _, m := range accessible {
		if m.Name == req.ModelName {
			hasAccess = true
			break
		}
	}
	if !hasAccess {
		http.Error(w, "forbidden: no access to model", http.StatusForbidden)
		return
	}

	result, err := h.secrets.CreateKey(r.Context(), req.ModelName, user.Username, req.Description)
	if err != nil {
		h.logger.Error("createKey failed", "error", err, "user", user.Username, "model", req.ModelName)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"clientId": result.ClientID,
		"apiKey":   result.APIKey,
	})
}

// deleteKey handles DELETE /api/keys/{namespace}/{model}/{clientID}.
func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	namespace := r.PathValue("namespace")
	modelName := r.PathValue("model")
	clientID := r.PathValue("clientID")

	// List all keys for the model to find and verify ownership.
	keys, err := h.secrets.ListKeys(r.Context(), modelName)
	if err != nil {
		h.logger.Error("deleteKey: ListKeys failed", "error", err, "user", user.Username, "model", modelName, "namespace", namespace, "clientID", clientID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Find the key by clientID and namespace.
	var found *secrets.KeyInfo
	for i := range keys {
		if keys[i].ClientID == clientID && keys[i].Namespace == namespace {
			found = &keys[i]
			break
		}
	}

	if found == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if found.Creator != user.Username {
		http.Error(w, "forbidden: key belongs to another user", http.StatusForbidden)
		return
	}

	if err := h.secrets.RevokeKey(r.Context(), modelName, clientID); err != nil {
		h.logger.Error("deleteKey: RevokeKey failed", "error", err, "user", user.Username, "model", modelName, "namespace", namespace, "clientID", clientID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
