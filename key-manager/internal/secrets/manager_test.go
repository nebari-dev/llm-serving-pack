package secrets_test

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// sanitizedPrefix mirrors the production sanitizeUsernameForKey helper so
// black-box tests can assemble the exact clientID the manager will produce
// without exporting the helper. Kept as an independent implementation so a
// drift between this and production fails the integration tests below.
func sanitizedPrefix(raw string) string {
	if raw == "" {
		return ""
	}
	allValid := true
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		valid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_'
		if !valid {
			allValid = false
			break
		}
	}
	if allValid {
		return raw
	}
	out := make([]byte, 0, len(raw)+9)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		valid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_'
		switch {
		case c == '@':
			out = append(out, "-at-"...)
		case valid:
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(raw))
	return fmt.Sprintf("%s-%08x", string(out), h.Sum32())
}

// pairSuffix mirrors the production (username, model) pair hash appended to
// every clientID so black-box tests can assemble the exact clientID the
// manager will produce. Kept as an independent implementation so a drift
// between this and production fails the tests below.
func pairSuffix(rawUser, rawModel string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(rawUser))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(rawModel))
	return fmt.Sprintf("%08x", h.Sum32())
}

const testNamespace = "llm-api-keys"

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

// makeSecret creates a pre-existing empty Secret as the operator would have created it.
func makeSecret(modelName, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      modelName + "-api-keys",
			Namespace: namespace,
			Labels: map[string]string{
				"llm.nebari.dev/model-name": modelName,
			},
		},
		Data: map[string][]byte{},
	}
}

// makeConfigMap creates a pre-existing empty ConfigMap as the operator would have created it.
func makeConfigMap(modelName, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      modelName + "-api-key-metadata",
			Namespace: namespace,
			Labels: map[string]string{
				"llm.nebari.dev/model-name": modelName,
			},
		},
		Data: map[string]string{},
	}
}

func TestCreateKey(t *testing.T) {
	tests := []struct {
		name        string
		modelName   string
		username    string
		description string
	}{
		{
			name:        "generates sk- prefixed key and writes to Secret and ConfigMap",
			modelName:   "my-model",
			username:    "chuck",
			description: "test key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(
					makeSecret(tc.modelName, testNamespace),
					makeConfigMap(tc.modelName, testNamespace),
				).
				Build()

			mgr := secrets.NewManager(fakeClient, testNamespace)
			result, err := mgr.CreateKey(context.Background(), tc.modelName, tc.username, tc.description)
			if err != nil {
				t.Fatalf("CreateKey error: %v", err)
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if !strings.HasPrefix(result.APIKey, "sk-") {
				t.Errorf("expected APIKey with prefix sk-, got %q", result.APIKey)
			}
			// Verify key length: "sk-" + 32 base64url chars = 35 chars
			if len(result.APIKey) != 35 {
				t.Errorf("expected APIKey length 35, got %d: %q", len(result.APIKey), result.APIKey)
			}

			// Verify key was written to the Secret
			secret := &corev1.Secret{}
			secretKey := types.NamespacedName{Namespace: testNamespace, Name: tc.modelName + "-api-keys"}
			if err := fakeClient.Get(context.Background(), secretKey, secret); err != nil {
				t.Fatalf("Get Secret: %v", err)
			}
			if _, ok := secret.Data[result.ClientID]; !ok {
				t.Errorf("expected clientID %q in Secret data", result.ClientID)
			}
			if string(secret.Data[result.ClientID]) != result.APIKey {
				t.Errorf("expected Secret data[%q]=%q, got %q", result.ClientID, result.APIKey, string(secret.Data[result.ClientID]))
			}

			// Verify metadata was written to ConfigMap
			cm := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{Namespace: testNamespace, Name: tc.modelName + "-api-key-metadata"}
			if err := fakeClient.Get(context.Background(), cmKey, cm); err != nil {
				t.Fatalf("Get ConfigMap: %v", err)
			}
			if _, ok := cm.Data[result.ClientID]; !ok {
				t.Errorf("expected clientID %q in ConfigMap data", result.ClientID)
			}
		})
	}
}

func TestCreateKey_ClientIDFormat(t *testing.T) {
	tests := []struct {
		name         string
		username     string
		existingKeys []string // clientIDs already in the secret for this user
		wantClientID string
	}{
		{
			name:         "first key for user is -1",
			username:     "chuck",
			existingKeys: []string{},
			wantClientID: "user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-1",
		},
		{
			name:         "second key for user is -2",
			username:     "chuck",
			existingKeys: []string{"user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-1"},
			wantClientID: "user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-2",
		},
		{
			name:     "third key for user is -3",
			username: "chuck",
			existingKeys: []string{
				"user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-1",
				"user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-2",
			},
			wantClientID: "user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)

			// Build pre-existing secret data with existing keys
			secretData := map[string][]byte{}
			for _, k := range tc.existingKeys {
				secretData[k] = []byte("sk-existingkey00000000000000000000")
			}
			cmData := map[string]string{}
			for _, k := range tc.existingKeys {
				cmData[k] = `{"clientId":"` + k + `","creator":"chuck","description":"","createdAt":"2024-01-01T00:00:00Z","modelName":"my-model","namespace":"llm-api-keys"}`
			}

			secret := makeSecret("my-model", testNamespace)
			secret.Data = secretData
			cm := makeConfigMap("my-model", testNamespace)
			cm.Data = cmData

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(secret, cm).
				Build()

			mgr := secrets.NewManager(fakeClient, testNamespace)
			result, err := mgr.CreateKey(context.Background(), "my-model", tc.username, "test")
			if err != nil {
				t.Fatalf("CreateKey error: %v", err)
			}
			if result.ClientID != tc.wantClientID {
				t.Errorf("expected ClientID %q, got %q", tc.wantClientID, result.ClientID)
			}
		})
	}
}

// TestCreateKey_ClientIDUniqueAcrossModels is a regression test for the
// per-model API-key scoping bug: a single user minting one key for each of two
// models must get DISTINCT client IDs. Before the fix the sequence was counted
// per-model, so the first key of every model was "user-<name>-1"; once the
// operator pooled every model's api-keys Secret into each model's
// SecurityPolicy credentialRefs, those duplicate client IDs collided - one
// model's key authenticated/authorized for another, and the user's other keys
// failed to authenticate at all.
func TestCreateKey_ClientIDUniqueAcrossModels(t *testing.T) {
	scheme := buildScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			makeSecret("model-a", testNamespace),
			makeConfigMap("model-a", testNamespace),
			makeSecret("model-b", testNamespace),
			makeConfigMap("model-b", testNamespace),
		).
		Build()

	mgr := secrets.NewManager(fakeClient, testNamespace)
	a, err := mgr.CreateKey(context.Background(), "model-a", "chuck", "test")
	if err != nil {
		t.Fatalf("CreateKey model-a: %v", err)
	}
	b, err := mgr.CreateKey(context.Background(), "model-b", "chuck", "test")
	if err != nil {
		t.Fatalf("CreateKey model-b: %v", err)
	}
	if a.ClientID == b.ClientID {
		t.Errorf("client IDs collide across models: model-a=%q model-b=%q (must be unique)", a.ClientID, b.ClientID)
	}
	wantA := "user-chuck-model-a-" + pairSuffix("chuck", "model-a") + "-1"
	if a.ClientID != wantA {
		t.Errorf("model-a clientID = %q, want %q", a.ClientID, wantA)
	}
	wantB := "user-chuck-model-b-" + pairSuffix("chuck", "model-b") + "-1"
	if b.ClientID != wantB {
		t.Errorf("model-b clientID = %q, want %q", b.ClientID, wantB)
	}
}

// TestCreateKey_ClientIDUnambiguousBoundary: usernames and model names both
// legitimately contain hyphens, so composing "user-<name>-<model>-<n>" with a
// hyphen separator is ambiguous: ("mary", "jane-chat") and ("mary-jane",
// "chat") would compose to the same clientID. Duplicate client IDs across two
// models' Secrets collide in the pooled credentialRefs set (the #122 failure
// mode: cross-model authorization plus one key failing to authenticate), so
// distinct (user, model) pairs must always mint distinct client IDs.
func TestCreateKey_ClientIDUnambiguousBoundary(t *testing.T) {
	scheme := buildScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			makeSecret("jane-chat", testNamespace),
			makeConfigMap("jane-chat", testNamespace),
			makeSecret("chat", testNamespace),
			makeConfigMap("chat", testNamespace),
		).
		Build()

	mgr := secrets.NewManager(fakeClient, testNamespace)
	a, err := mgr.CreateKey(context.Background(), "jane-chat", "mary", "test")
	if err != nil {
		t.Fatalf("CreateKey mary/jane-chat: %v", err)
	}
	b, err := mgr.CreateKey(context.Background(), "chat", "mary-jane", "test")
	if err != nil {
		t.Fatalf("CreateKey mary-jane/chat: %v", err)
	}
	if a.ClientID == b.ClientID {
		t.Errorf("distinct (user, model) pairs minted the same clientID %q; pooled credentialRefs require globally unique client IDs", a.ClientID)
	}
}

// TestCreateKey_RevokeThenMintDoesNotTouchLiveKeys: the sequence number must
// not be derived from a bare count of existing keys. With keys {1,2,3},
// revoking #2 leaves a count of 2, so a count-based mint would reuse sequence
// 3 and silently overwrite the still-live key #3 (Secret value and ConfigMap
// metadata), killing a credential the user believes is active.
func TestCreateKey_RevokeThenMintDoesNotTouchLiveKeys(t *testing.T) {
	scheme := buildScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			makeSecret("model-a", testNamespace),
			makeConfigMap("model-a", testNamespace),
		).
		Build()

	ctx := context.Background()
	mgr := secrets.NewManager(fakeClient, testNamespace)
	minted := make([]*secrets.CreateKeyResult, 0, 3)
	for range 3 {
		r, err := mgr.CreateKey(ctx, "model-a", "chuck", "test")
		if err != nil {
			t.Fatalf("CreateKey: %v", err)
		}
		minted = append(minted, r)
	}

	if err := mgr.RevokeKey(ctx, "model-a", minted[1].ClientID); err != nil {
		t.Fatalf("RevokeKey %s: %v", minted[1].ClientID, err)
	}
	live := []*secrets.CreateKeyResult{minted[0], minted[2]}

	fresh, err := mgr.CreateKey(ctx, "model-a", "chuck", "test")
	if err != nil {
		t.Fatalf("CreateKey after revoke: %v", err)
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: testNamespace, Name: "model-a-api-keys"}
	if err := fakeClient.Get(ctx, key, secret); err != nil {
		t.Fatalf("Get Secret: %v", err)
	}
	for _, l := range live {
		if fresh.ClientID == l.ClientID {
			t.Errorf("mint after revoke reused live clientID %q", l.ClientID)
		}
		if got := string(secret.Data[l.ClientID]); got != l.APIKey {
			t.Errorf("live key %q value changed after mint: got %q, want %q", l.ClientID, got, l.APIKey)
		}
	}
	if _, ok := secret.Data[fresh.ClientID]; !ok {
		t.Errorf("freshly minted clientID %q missing from Secret", fresh.ClientID)
	}
}

func TestCreateKey_EmptyUsername(t *testing.T) {
	scheme := buildScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			makeSecret("my-model", testNamespace),
			makeConfigMap("my-model", testNamespace),
		).
		Build()

	mgr := secrets.NewManager(fakeClient, testNamespace)
	_, err := mgr.CreateKey(context.Background(), "my-model", "", "test")
	if err == nil {
		t.Fatal("expected error for empty username, got nil")
	}
	if !strings.Contains(err.Error(), "username") {
		t.Errorf("expected error to mention username, got: %v", err)
	}
}

func TestCreateKey_SecretNotFound(t *testing.T) {
	scheme := buildScheme(t)
	// Only create ConfigMap, not Secret - simulates operator not having created the secret yet
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(makeConfigMap("missing-model", testNamespace)).
		Build()

	mgr := secrets.NewManager(fakeClient, testNamespace)
	_, err := mgr.CreateKey(context.Background(), "missing-model", "chuck", "test")
	if err == nil {
		t.Fatal("expected error when Secret does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "missing-model") {
		t.Errorf("expected error to mention model name, got: %v", err)
	}
}

func TestListKeys(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		cmData    map[string]string
		wantCount int
	}{
		{
			name:      "returns metadata for all keys of a model",
			modelName: "my-model",
			cmData: map[string]string{
				"user-alice-1": `{"clientId":"user-alice-1","creator":"alice","description":"alice key","createdAt":"2024-01-01T00:00:00Z","modelName":"my-model","namespace":"llm-api-keys"}`,
				"user-bob-1":   `{"clientId":"user-bob-1","creator":"bob","description":"bob key","createdAt":"2024-01-02T00:00:00Z","modelName":"my-model","namespace":"llm-api-keys"}`,
			},
			wantCount: 2,
		},
		{
			name:      "returns empty list when ConfigMap has no entries",
			modelName: "empty-model",
			cmData:    map[string]string{},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)

			cm := makeConfigMap(tc.modelName, testNamespace)
			cm.Data = tc.cmData

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(
					makeSecret(tc.modelName, testNamespace),
					cm,
				).
				Build()

			mgr := secrets.NewManager(fakeClient, testNamespace)
			keys, err := mgr.ListKeys(context.Background(), tc.modelName)
			if err != nil {
				t.Fatalf("ListKeys error: %v", err)
			}
			if len(keys) != tc.wantCount {
				t.Errorf("expected %d keys, got %d", tc.wantCount, len(keys))
			}
		})
	}
}

func TestListKeysForUser(t *testing.T) {
	tests := []struct {
		name      string
		models    []string
		cmDatas   map[string]map[string]string // modelName -> cmData
		username  string
		wantCount int
	}{
		{
			name:   "returns keys created by user across multiple models",
			models: []string{"model-a", "model-b"},
			cmDatas: map[string]map[string]string{
				"model-a": {
					"user-chuck-1": `{"clientId":"user-chuck-1","creator":"chuck","description":"","createdAt":"2024-01-01T00:00:00Z","modelName":"model-a","namespace":"llm-api-keys"}`,
					"user-alice-1": `{"clientId":"user-alice-1","creator":"alice","description":"","createdAt":"2024-01-01T00:00:00Z","modelName":"model-a","namespace":"llm-api-keys"}`,
				},
				"model-b": {
					"user-chuck-1": `{"clientId":"user-chuck-1","creator":"chuck","description":"","createdAt":"2024-01-02T00:00:00Z","modelName":"model-b","namespace":"llm-api-keys"}`,
				},
			},
			username:  "chuck",
			wantCount: 2,
		},
		{
			name:   "user with no keys returns empty list",
			models: []string{"model-a"},
			cmDatas: map[string]map[string]string{
				"model-a": {
					"user-alice-1": `{"clientId":"user-alice-1","creator":"alice","description":"","createdAt":"2024-01-01T00:00:00Z","modelName":"model-a","namespace":"llm-api-keys"}`,
				},
			},
			username:  "chuck",
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)

			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, modelName := range tc.models {
				cm := makeConfigMap(modelName, testNamespace)
				if data, ok := tc.cmDatas[modelName]; ok {
					cm.Data = data
				}
				builder = builder.WithObjects(makeSecret(modelName, testNamespace), cm)
			}
			fakeClient := builder.Build()

			mgr := secrets.NewManager(fakeClient, testNamespace)
			keys, err := mgr.ListKeysForUser(context.Background(), tc.username)
			if err != nil {
				t.Fatalf("ListKeysForUser error: %v", err)
			}
			if len(keys) != tc.wantCount {
				t.Errorf("expected %d keys for user %q, got %d", tc.wantCount, tc.username, len(keys))
			}
		})
	}
}

func TestRevokeKey(t *testing.T) {
	tests := []struct {
		name       string
		modelName  string
		clientID   string
		secretData map[string][]byte
		cmData     map[string]string
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:      "removes key from Secret and ConfigMap",
			modelName: "my-model",
			clientID:  "user-chuck-1",
			secretData: map[string][]byte{
				"user-chuck-1": []byte("sk-somekey00000000000000000000000000"),
			},
			cmData: map[string]string{
				"user-chuck-1": `{"clientId":"user-chuck-1","creator":"chuck","description":"","createdAt":"2024-01-01T00:00:00Z","modelName":"my-model","namespace":"llm-api-keys"}`,
			},
			wantErr: false,
		},
		{
			name:       "returns error if key does not exist",
			modelName:  "my-model",
			clientID:   "user-nobody-1",
			secretData: map[string][]byte{},
			cmData:     map[string]string{},
			wantErr:    true,
			wantErrMsg: "user-nobody-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)

			secret := makeSecret(tc.modelName, testNamespace)
			secret.Data = tc.secretData
			cm := makeConfigMap(tc.modelName, testNamespace)
			cm.Data = tc.cmData

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(secret, cm).
				Build()

			mgr := secrets.NewManager(fakeClient, testNamespace)
			err := mgr.RevokeKey(context.Background(), tc.modelName, tc.clientID)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantErrMsg != "" && !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Errorf("expected error to contain %q, got: %v", tc.wantErrMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RevokeKey error: %v", err)
			}

			// Verify removed from Secret
			updatedSecret := &corev1.Secret{}
			secretKey := types.NamespacedName{Namespace: testNamespace, Name: tc.modelName + "-api-keys"}
			if err := fakeClient.Get(context.Background(), secretKey, updatedSecret); err != nil {
				t.Fatalf("Get Secret after revoke: %v", err)
			}
			if _, ok := updatedSecret.Data[tc.clientID]; ok {
				t.Errorf("expected clientID %q to be removed from Secret", tc.clientID)
			}

			// Verify removed from ConfigMap
			updatedCM := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{Namespace: testNamespace, Name: tc.modelName + "-api-key-metadata"}
			if err := fakeClient.Get(context.Background(), cmKey, updatedCM); err != nil {
				t.Fatalf("Get ConfigMap after revoke: %v", err)
			}
			if _, ok := updatedCM.Data[tc.clientID]; ok {
				t.Errorf("expected clientID %q to be removed from ConfigMap", tc.clientID)
			}
		})
	}
}

func TestCreateKey_SanitizesEmailUsername(t *testing.T) {
	tests := []struct {
		name             string
		username         string
		wantClientID     string
		wantCreatorRaw   string
		existingDataKeys []string
	}{
		{
			name:           "email username gets @ replaced with -at- and a hash suffix",
			username:       "alice@example.com",
			wantClientID:   "user-" + sanitizedPrefix("alice@example.com") + "-my-model-" + pairSuffix("alice@example.com", "my-model") + "-1",
			wantCreatorRaw: "alice@example.com",
		},
		{
			name:           "plain alphanumeric username is unchanged",
			username:       "chuck",
			wantClientID:   "user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-1",
			wantCreatorRaw: "chuck",
		},
		{
			name:             "sequence increments using sanitized prefix",
			username:         "alice@example.com",
			wantClientID:     "user-" + sanitizedPrefix("alice@example.com") + "-my-model-" + pairSuffix("alice@example.com", "my-model") + "-2",
			wantCreatorRaw:   "alice@example.com",
			existingDataKeys: []string{"user-" + sanitizedPrefix("alice@example.com") + "-my-model-" + pairSuffix("alice@example.com", "my-model") + "-1"},
		},
		{
			name:           "raw alice-at-example.com does NOT collide with alice@example.com",
			username:       "alice-at-example.com",
			wantClientID:   "user-alice-at-example.com-my-model-" + pairSuffix("alice-at-example.com", "my-model") + "-1", // already-valid username -> no sanitizer hash suffix
			wantCreatorRaw: "alice-at-example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)

			secretData := map[string][]byte{}
			for _, k := range tc.existingDataKeys {
				secretData[k] = []byte("sk-existingkey00000000000000000000")
			}
			cmData := map[string]string{}
			for _, k := range tc.existingDataKeys {
				cmData[k] = `{"clientId":"` + k + `","creator":"` + tc.username + `","description":"","createdAt":"2024-01-01T00:00:00Z","modelName":"my-model","namespace":"llm-api-keys"}`
			}

			secret := makeSecret("my-model", testNamespace)
			secret.Data = secretData
			cm := makeConfigMap("my-model", testNamespace)
			cm.Data = cmData

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(secret, cm).
				Build()

			mgr := secrets.NewManager(fakeClient, testNamespace)
			result, err := mgr.CreateKey(context.Background(), "my-model", tc.username, "test")
			if err != nil {
				t.Fatalf("CreateKey error: %v", err)
			}
			if result.ClientID != tc.wantClientID {
				t.Errorf("expected ClientID %q, got %q", tc.wantClientID, result.ClientID)
			}

			// Verify the data key landed in the Secret with the sanitized form.
			updatedSecret := &corev1.Secret{}
			secretKey := types.NamespacedName{Namespace: testNamespace, Name: "my-model-api-keys"}
			if err := fakeClient.Get(context.Background(), secretKey, updatedSecret); err != nil {
				t.Fatalf("Get Secret: %v", err)
			}
			if _, ok := updatedSecret.Data[tc.wantClientID]; !ok {
				t.Errorf("expected sanitized clientID %q in Secret data, keys: %v", tc.wantClientID, mapKeys(updatedSecret.Data))
			}

			// Verify Creator preserves the raw username (so ListKeysForUser
			// matches against the SSO identity, not the mangled key form).
			updatedCM := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{Namespace: testNamespace, Name: "my-model-api-key-metadata"}
			if err := fakeClient.Get(context.Background(), cmKey, updatedCM); err != nil {
				t.Fatalf("Get ConfigMap: %v", err)
			}
			raw, ok := updatedCM.Data[tc.wantClientID]
			if !ok {
				t.Fatalf("expected sanitized clientID %q in ConfigMap data", tc.wantClientID)
			}
			if !strings.Contains(raw, `"creator":"`+tc.wantCreatorRaw+`"`) {
				t.Errorf("expected ConfigMap entry to record creator %q verbatim, got: %s", tc.wantCreatorRaw, raw)
			}

			// And the round-trip via ListKeysForUser using the raw username
			// must find the key.
			found, err := mgr.ListKeysForUser(context.Background(), tc.username)
			if err != nil {
				t.Fatalf("ListKeysForUser error: %v", err)
			}
			if len(found) == 0 {
				t.Errorf("ListKeysForUser(%q) returned 0 keys, want >=1", tc.username)
			}
		})
	}
}

// mapKeys returns the keys of m as a slice (for diagnostic output).
func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCreateKeyThenListKeys(t *testing.T) {
	scheme := buildScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			makeSecret("my-model", testNamespace),
			makeConfigMap("my-model", testNamespace),
		).
		Build()

	mgr := secrets.NewManager(fakeClient, testNamespace)

	before := time.Now().Truncate(time.Second)
	result, err := mgr.CreateKey(context.Background(), "my-model", "chuck", "integration test key")
	after := time.Now().Add(time.Second)
	if err != nil {
		t.Fatalf("CreateKey error: %v", err)
	}

	keys, err := mgr.ListKeys(context.Background(), "my-model")
	if err != nil {
		t.Fatalf("ListKeys error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}

	key := keys[0]
	if key.ClientID != result.ClientID {
		t.Errorf("expected ClientID %q, got %q", result.ClientID, key.ClientID)
	}
	if key.Creator != "chuck" {
		t.Errorf("expected Creator=chuck, got %q", key.Creator)
	}
	if key.Description != "integration test key" {
		t.Errorf("expected Description=%q, got %q", "integration test key", key.Description)
	}
	if key.ModelName != "my-model" {
		t.Errorf("expected ModelName=my-model, got %q", key.ModelName)
	}
	if key.Namespace != testNamespace {
		t.Errorf("expected Namespace=%q, got %q", testNamespace, key.Namespace)
	}
	if key.CreatedAt.Before(before) || key.CreatedAt.After(after) {
		t.Errorf("CreatedAt %v is outside the expected range [%v, %v]", key.CreatedAt, before, after)
	}
}
