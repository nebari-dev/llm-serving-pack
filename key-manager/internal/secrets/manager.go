package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KeyInfo contains metadata about an API key (never the key value itself).
type KeyInfo struct {
	ClientID    string    `json:"clientId"`
	Creator     string    `json:"creator"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	ModelName   string    `json:"modelName"`
	Namespace   string    `json:"namespace"`
}

// CreateKeyResult is returned when a new key is created.
type CreateKeyResult struct {
	ClientID string // e.g., "user-chuck-1"
	APIKey   string // e.g., "sk-abc123..." - only returned once at creation time
}

// Manager handles API key CRUD via K8s Secrets and ConfigMaps.
type Manager struct {
	client    client.Client
	namespace string // the llm-api-keys namespace
}

// NewManager creates a new Manager.
func NewManager(c client.Client, namespace string) *Manager {
	return &Manager{client: c, namespace: namespace}
}

// secretName returns the name of the Secret for a model.
func secretName(modelName string) string {
	return modelName + "-api-keys"
}

// sanitizeUsernameForKey converts a username into a form that is valid as a
// Kubernetes Secret/ConfigMap data key. K8s data keys must match
// [-._a-zA-Z0-9]+, so SSO email usernames (e.g. "alice@example.com") would
// otherwise cause the API server to reject the update.
//
// Already-valid input is returned unchanged so the common ASCII case keeps
// human-readable clientIDs. When sanitization is needed, "@" becomes "-at-"
// and any other disallowed byte becomes "-"; an 8-hex-char FNV-1a hash of
// the raw username is then appended so two distinct raw usernames that
// would otherwise collide (e.g. "alice@example.com" vs literal
// "alice-at-example.com") get unique keys.
//
// The original (unsanitized) username is still recorded in KeyInfo.Creator
// so ownership lookups work against the raw value. Empty input returns ""
// and must be rejected by the caller before composing a data key.
func sanitizeUsernameForKey(username string) string {
	if username == "" {
		return ""
	}
	// Fast path: if every byte is already valid, return as-is to avoid
	// allocating and to keep clientIDs human-readable for ASCII users.
	if isValidDataKeyName(username) {
		return username
	}
	var b strings.Builder
	b.Grow(len(username) + 9) // worst case "@"-only inputs grow ~4x; "+ -XXXXXXXX" suffix is 9
	for i := 0; i < len(username); i++ {
		c := username[i]
		switch {
		case c == '@':
			b.WriteString("-at-")
		case isValidDataKeyByte(c):
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(username))
	fmt.Fprintf(&b, "-%08x", h.Sum32())
	return b.String()
}

// isValidDataKeyByte reports whether c is allowed in a Kubernetes
// Secret/ConfigMap data key.
func isValidDataKeyByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '-' || c == '.' || c == '_':
		return true
	}
	return false
}

// isValidDataKeyName reports whether s contains only bytes allowed in a
// Kubernetes Secret/ConfigMap data key.
func isValidDataKeyName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isValidDataKeyByte(s[i]) {
			return false
		}
	}
	return true
}

// configMapName returns the name of the ConfigMap for a model.
func configMapName(modelName string) string {
	return modelName + "-api-key-metadata"
}

// generateAPIKey generates a new API key with sk- prefix and 32 random base64url characters.
func generateAPIKey() (string, error) {
	// 24 random bytes => 32 base64url characters (no padding)
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	return "sk-" + encoded, nil
}

// getSecret fetches the Secret for a model, returning a descriptive error if not found.
func (m *Manager) getSecret(ctx context.Context, modelName string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: m.namespace, Name: secretName(modelName)}
	if err := m.client.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("API key secret not found for model %s - has the operator created it?: %w", modelName, err)
	}
	return secret, nil
}

// getConfigMap fetches the ConfigMap for a model, returning a descriptive error if not found.
func (m *Manager) getConfigMap(ctx context.Context, modelName string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: m.namespace, Name: configMapName(modelName)}
	if err := m.client.Get(ctx, key, cm); err != nil {
		return nil, fmt.Errorf("API key metadata ConfigMap not found for model %s - has the operator created it?: %w", modelName, err)
	}
	return cm, nil
}

// CreateKey generates a new API key for a model, writes it to the Secret,
// and stores metadata in the ConfigMap. Returns the key (only time it's shown).
func (m *Manager) CreateKey(ctx context.Context, modelName, username, description string) (*CreateKeyResult, error) {
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	const maxRetries = 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err := m.createKeyOnce(ctx, modelName, username, description)
		if err == nil {
			return result, nil
		}
		// On conflict, retry; on other errors, return immediately.
		if !isConflict(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("creating key for model %s: too many conflicts", modelName)
}

func (m *Manager) createKeyOnce(ctx context.Context, modelName, username, description string) (*CreateKeyResult, error) {
	secret, err := m.getSecret(ctx, modelName)
	if err != nil {
		return nil, err
	}
	cm, err := m.getConfigMap(ctx, modelName)
	if err != nil {
		return nil, err
	}

	// Count existing keys for this (user, model) to determine the sequence
	// number. The clientID is scoped by BOTH username and model: the operator
	// pools every model's api-keys Secret into each model's SecurityPolicy
	// credentialRefs (model-scoped auth), and forwards the matched data key as
	// the x-llm-client-id used for per-model authorization. A clientID that
	// repeats across models - e.g. "user-<name>-1" for the first key of every
	// model - collides in that pooled set, so one model's key both
	// authenticates and authorizes for another, and the user's other keys fail
	// to authenticate at all (only one value survives per duplicated key).
	// Including the model keeps each clientID globally unique.
	// KeyInfo.Creator below preserves the raw username for ownership checks.
	safeUsername := sanitizeUsernameForKey(username)
	safeModel := sanitizeUsernameForKey(modelName)
	userPrefix := "user-" + safeUsername + "-" + safeModel + "-"
	count := 0
	for k := range secret.Data {
		if strings.HasPrefix(k, userPrefix) {
			count++
		}
	}
	sequence := count + 1
	clientID := fmt.Sprintf("user-%s-%s-%d", safeUsername, safeModel, sequence)

	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("generating API key: %w", err)
	}

	// Write the key to the Secret.
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data[clientID] = []byte(apiKey)
	if err := m.client.Update(ctx, secret); err != nil {
		return nil, fmt.Errorf("updating Secret for model %s: %w", modelName, err)
	}

	// Write metadata to the ConfigMap.
	info := KeyInfo{
		ClientID:    clientID,
		Creator:     username,
		Description: description,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
		ModelName:   modelName,
		Namespace:   m.namespace,
	}
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshaling key metadata: %w", err)
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[clientID] = string(infoJSON)
	if err := m.client.Update(ctx, cm); err != nil {
		return nil, fmt.Errorf("updating ConfigMap for model %s: %w", modelName, err)
	}

	return &CreateKeyResult{
		ClientID: clientID,
		APIKey:   apiKey,
	}, nil
}

// ListKeys returns metadata for all keys belonging to a model.
func (m *Manager) ListKeys(ctx context.Context, modelName string) ([]KeyInfo, error) {
	cm, err := m.getConfigMap(ctx, modelName)
	if err != nil {
		return nil, err
	}
	return parseConfigMapKeys(cm), nil
}

// ListKeysForUser returns metadata for keys created by a specific user across all models.
func (m *Manager) ListKeysForUser(ctx context.Context, username string) ([]KeyInfo, error) {
	// List all ConfigMaps in the namespace and filter by label/name to find API key metadata ConfigMaps.
	allCMs := &corev1.ConfigMapList{}
	if err := m.client.List(ctx, allCMs, client.InNamespace(m.namespace)); err != nil {
		return nil, fmt.Errorf("listing ConfigMaps: %w", err)
	}

	var result []KeyInfo
	for i := range allCMs.Items {
		cm := &allCMs.Items[i]
		// Only process ConfigMaps that are API key metadata ConfigMaps (have the label).
		if _, ok := cm.Labels["llm.nebari.dev/model-name"]; !ok {
			continue
		}
		// Only process ConfigMaps with the -api-key-metadata suffix.
		if !strings.HasSuffix(cm.Name, "-api-key-metadata") {
			continue
		}
		for _, info := range parseConfigMapKeys(cm) {
			if info.Creator == username {
				result = append(result, info)
			}
		}
	}
	return result, nil
}

// RevokeKey removes a key from the Secret and its metadata from the ConfigMap.
func (m *Manager) RevokeKey(ctx context.Context, modelName, clientID string) error {
	const maxRetries = 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := m.revokeKeyOnce(ctx, modelName, clientID)
		if err == nil {
			return nil
		}
		if !isConflict(err) {
			return err
		}
	}
	return fmt.Errorf("revoking key %s for model %s: too many conflicts", clientID, modelName)
}

func (m *Manager) revokeKeyOnce(ctx context.Context, modelName, clientID string) error {
	secret, err := m.getSecret(ctx, modelName)
	if err != nil {
		return err
	}
	cm, err := m.getConfigMap(ctx, modelName)
	if err != nil {
		return err
	}

	// Check the key exists before attempting removal.
	if _, ok := secret.Data[clientID]; !ok {
		return fmt.Errorf("key %q not found for model %s", clientID, modelName)
	}

	delete(secret.Data, clientID)
	if err := m.client.Update(ctx, secret); err != nil {
		return fmt.Errorf("updating Secret for model %s: %w", modelName, err)
	}

	delete(cm.Data, clientID)
	if err := m.client.Update(ctx, cm); err != nil {
		return fmt.Errorf("updating ConfigMap for model %s: %w", modelName, err)
	}

	return nil
}

// parseConfigMapKeys parses all KeyInfo entries from a ConfigMap's data map.
func parseConfigMapKeys(cm *corev1.ConfigMap) []KeyInfo {
	var result []KeyInfo
	for _, v := range cm.Data {
		var info KeyInfo
		if err := json.Unmarshal([]byte(v), &info); err != nil {
			continue
		}
		result = append(result, info)
	}
	return result
}

// isConflict returns true if the error is a Kubernetes conflict (409).
func isConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "conflict") ||
		strings.Contains(err.Error(), "409")
}
