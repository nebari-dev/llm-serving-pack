package secrets_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
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
	return fmt.Sprintf("%s-%s", string(out), testShortHash(raw))
}

// pairSuffix mirrors the production (username, model) pair hash appended to
// every clientID so black-box tests can assemble the exact clientID the
// manager will produce. Kept as an independent implementation so a drift
// between this and production fails the tests below.
func pairSuffix(rawUser, rawModel string) string {
	return testShortHash(rawUser, rawModel)
}

// checkClientID fails t unless got is prefix followed by the 16-hex random
// part every client ID ends with.
func checkClientID(t *testing.T, got, prefix string) {
	t.Helper()
	suffix, ok := strings.CutPrefix(got, prefix)
	if _, err := hex.DecodeString(suffix); !ok || len(suffix) != 16 || err != nil {
		t.Errorf("client ID = %q, want %q followed by 16 random hex digits", got, prefix)
	}
}

// testShortHash mirrors the production shortHash helper (128-bit-truncated
// SHA-256 over NUL-joined parts). Kept as an independent implementation so a
// drift between this and production fails the tests below.
func testShortHash(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
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
			result, err := mgr.CreateKey(context.Background(), tc.modelName, secrets.Creator{Username: tc.username}, tc.description)
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
	prefix := "user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-"
	existing := prefix + "0123456789abcdef"
	secret := makeSecret("my-model", testNamespace)
	secret.Data = map[string][]byte{existing: []byte("sk-existingkey00000000000000000000")}
	cm := makeConfigMap("my-model", testNamespace)
	cm.Data = map[string]string{existing: `{"clientId":"` + existing + `","creator":"chuck","description":"","createdAt":"2024-01-01T00:00:00Z","modelName":"my-model","namespace":"llm-api-keys"}`}
	fakeClient := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(secret, cm).Build()

	result, err := secrets.NewManager(fakeClient, testNamespace).CreateKey(context.Background(), "my-model", secrets.Creator{Username: "chuck"}, "test")
	if err != nil {
		t.Fatalf("CreateKey error: %v", err)
	}
	checkClientID(t, result.ClientID, prefix)
	if result.ClientID == existing {
		t.Errorf("new key reused the existing client ID %q", existing)
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
	a, err := mgr.CreateKey(context.Background(), "model-a", secrets.Creator{Username: "chuck"}, "test")
	if err != nil {
		t.Fatalf("CreateKey model-a: %v", err)
	}
	b, err := mgr.CreateKey(context.Background(), "model-b", secrets.Creator{Username: "chuck"}, "test")
	if err != nil {
		t.Fatalf("CreateKey model-b: %v", err)
	}
	if a.ClientID == b.ClientID {
		t.Errorf("client IDs collide across models: model-a=%q model-b=%q (must be unique)", a.ClientID, b.ClientID)
	}
	checkClientID(t, a.ClientID, "user-chuck-model-a-"+pairSuffix("chuck", "model-a")+"-")
	checkClientID(t, b.ClientID, "user-chuck-model-b-"+pairSuffix("chuck", "model-b")+"-")
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
	a, err := mgr.CreateKey(context.Background(), "jane-chat", secrets.Creator{Username: "mary"}, "test")
	if err != nil {
		t.Fatalf("CreateKey mary/jane-chat: %v", err)
	}
	b, err := mgr.CreateKey(context.Background(), "chat", secrets.Creator{Username: "mary-jane"}, "test")
	if err != nil {
		t.Fatalf("CreateKey mary-jane/chat: %v", err)
	}
	if a.ClientID == b.ClientID {
		t.Errorf("distinct (user, model) pairs minted the same clientID %q; pooled credentialRefs require globally unique client IDs", a.ClientID)
	}
}

// TestCreateKey_RevokeThenMintDoesNotTouchLiveKeys: minting after a revoke
// must never take a live key's client ID, which would silently overwrite that
// key (Secret value and ConfigMap metadata) and kill a credential the user
// believes is active. A client ID numbered by a bare count of live keys did
// exactly that: with keys {1,2,3}, revoking #2 made the next mint #3.
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
		r, err := mgr.CreateKey(ctx, "model-a", secrets.Creator{Username: "chuck"}, "test")
		if err != nil {
			t.Fatalf("CreateKey: %v", err)
		}
		minted = append(minted, r)
	}

	if err := mgr.RevokeKey(ctx, "model-a", minted[1].ClientID); err != nil {
		t.Fatalf("RevokeKey %s: %v", minted[1].ClientID, err)
	}
	live := []*secrets.CreateKeyResult{minted[0], minted[2]}

	fresh, err := mgr.CreateKey(ctx, "model-a", secrets.Creator{Username: "chuck"}, "test")
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
	_, err := mgr.CreateKey(context.Background(), "my-model", secrets.Creator{Username: ""}, "test")
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
	_, err := mgr.CreateKey(context.Background(), "missing-model", secrets.Creator{Username: "chuck"}, "test")
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
			name:      "removes key from Secret and key listings",
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

			// Verify no longer listed (the ConfigMap keeps only its owner).
			keys, err := mgr.ListKeys(context.Background(), tc.modelName)
			if err != nil {
				t.Fatalf("ListKeys after revoke: %v", err)
			}
			for _, k := range keys {
				if k.ClientID == tc.clientID {
					t.Errorf("expected clientID %q to be no longer listed", tc.clientID)
				}
			}
		})
	}
}

func TestCreateKey_SanitizesEmailUsername(t *testing.T) {
	tests := []struct {
		name           string
		username       string
		wantPrefix     string
		wantCreatorRaw string
	}{
		{
			name:           "email username gets @ replaced with -at- and a hash suffix",
			username:       "alice@example.com",
			wantPrefix:     "user-" + sanitizedPrefix("alice@example.com") + "-my-model-" + pairSuffix("alice@example.com", "my-model") + "-",
			wantCreatorRaw: "alice@example.com",
		},
		{
			name:           "plain alphanumeric username is unchanged",
			username:       "chuck",
			wantPrefix:     "user-chuck-my-model-" + pairSuffix("chuck", "my-model") + "-",
			wantCreatorRaw: "chuck",
		},
		{
			name:           "raw alice-at-example.com does NOT collide with alice@example.com",
			username:       "alice-at-example.com",
			wantPrefix:     "user-alice-at-example.com-my-model-" + pairSuffix("alice-at-example.com", "my-model") + "-", // already-valid username -> no sanitizer hash suffix
			wantCreatorRaw: "alice-at-example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(buildScheme(t)).
				WithObjects(makeSecret("my-model", testNamespace), makeConfigMap("my-model", testNamespace)).
				Build()

			mgr := secrets.NewManager(fakeClient, testNamespace)
			result, err := mgr.CreateKey(context.Background(), "my-model", secrets.Creator{Username: tc.username}, "test")
			if err != nil {
				t.Fatalf("CreateKey error: %v", err)
			}
			checkClientID(t, result.ClientID, tc.wantPrefix)

			// Verify the data key landed in the Secret with the sanitized form.
			updatedSecret := &corev1.Secret{}
			secretKey := types.NamespacedName{Namespace: testNamespace, Name: "my-model-api-keys"}
			if err := fakeClient.Get(context.Background(), secretKey, updatedSecret); err != nil {
				t.Fatalf("Get Secret: %v", err)
			}
			if _, ok := updatedSecret.Data[result.ClientID]; !ok {
				t.Errorf("expected sanitized clientID %q in Secret data, keys: %v", result.ClientID, mapKeys(updatedSecret.Data))
			}

			// Verify Creator preserves the raw username (so ListKeysForUser
			// matches against the SSO identity, not the mangled key form).
			updatedCM := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{Namespace: testNamespace, Name: "my-model-api-key-metadata"}
			if err := fakeClient.Get(context.Background(), cmKey, updatedCM); err != nil {
				t.Fatalf("Get ConfigMap: %v", err)
			}
			raw, ok := updatedCM.Data[result.ClientID]
			if !ok {
				t.Fatalf("expected sanitized clientID %q in ConfigMap data", result.ClientID)
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
	result, err := mgr.CreateKey(context.Background(), "my-model", secrets.Creator{Username: "chuck"}, "integration test key")
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

// A username can be renamed and reused; the OIDC subject cannot. Usage made
// with a key is attributed to its creator's subject, so the key has to
// remember it.
func TestCreateKey_RemembersCreatorSubject(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(makeSecret("my-model", testNamespace), makeConfigMap("my-model", testNamespace)).
		Build()
	mgr := secrets.NewManager(fakeClient, testNamespace)

	created, err := mgr.CreateKey(context.Background(), "my-model",
		secrets.Creator{Username: "alice", Subject: "sub-alice"}, "test")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	keys, err := mgr.ListAllKeys(context.Background())
	if err != nil {
		t.Fatalf("ListAllKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].ClientID != created.ClientID {
		t.Fatalf("ListAllKeys = %+v, want the one key just created", keys)
	}
	if keys[0].Creator != "alice" || keys[0].CreatorSubject != "sub-alice" {
		t.Errorf("key creator = %q subject = %q, want alice / sub-alice", keys[0].Creator, keys[0].CreatorSubject)
	}
}

// Usage made with a key is reported per team as well as per person, and the
// team comes from the creator's groups at the time they minted the key.
func TestCreateKey_RemembersCreatorGroups(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(makeSecret("my-model", testNamespace), makeConfigMap("my-model", testNamespace)).
		Build()
	mgr := secrets.NewManager(fakeClient, testNamespace)

	if _, err := mgr.CreateKey(context.Background(), "my-model",
		secrets.Creator{Username: "alice", Subject: "sub-alice", Groups: []string{"llm-users", "team-a"}}, "test"); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	keys, err := mgr.ListAllKeys(context.Background())
	if err != nil {
		t.Fatalf("ListAllKeys: %v", err)
	}
	if len(keys) != 1 || strings.Join(keys[0].CreatorGroups, ",") != "llm-users,team-a" {
		t.Errorf("key creator groups = %v, want [llm-users team-a]", keys)
	}
}

// Usage records for a key's last requests can be processed after the key is
// revoked, and after the key manager restarts. Revoking keeps who minted the
// key in the metadata ConfigMap, so the owner can still be looked up, while
// the key itself is gone from every key listing.
func TestRevokeKey_KeepsWhoMintedTheKey(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(makeSecret("my-model", testNamespace), makeConfigMap("my-model", testNamespace)).
		Build()
	mgr := secrets.NewManager(fakeClient, testNamespace)
	ctx := context.Background()
	created, err := mgr.CreateKey(ctx, "my-model",
		secrets.Creator{Username: "alice", Subject: "sub-alice", Groups: []string{"team-a"}}, "notebook")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if err := mgr.RevokeKey(ctx, "my-model", created.ClientID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	if keys, _ := mgr.ListKeys(ctx, "my-model"); len(keys) != 0 {
		t.Errorf("ListKeys after revoke = %+v, want none", keys)
	}
	if keys, _ := mgr.ListAllKeys(ctx); len(keys) != 0 {
		t.Errorf("ListAllKeys after revoke = %+v, want none", keys)
	}
	if keys, _ := mgr.ListKeysForUser(ctx, "alice"); len(keys) != 0 {
		t.Errorf("ListKeysForUser after revoke = %+v, want none", keys)
	}
	all, err := mgr.ListAllKeysIncludingRevoked(ctx)
	if err != nil {
		t.Fatalf("ListAllKeysIncludingRevoked: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListAllKeysIncludingRevoked = %+v, want the revoked key", all)
	}
	k := all[0]
	if k.ClientID != created.ClientID || k.Creator != "alice" || k.CreatorSubject != "sub-alice" ||
		strings.Join(k.CreatorGroups, ",") != "team-a" || k.RevokedAt == nil {
		t.Errorf("revoked key = %+v, want alice / sub-alice / [team-a] with a revoke time", k)
	}
	if k.Description != "" {
		t.Errorf("revoked key keeps description %q, want it dropped", k.Description)
	}
	// A revoked key cannot be revoked twice.
	if err := mgr.RevokeKey(ctx, "my-model", created.ClientID); err == nil {
		t.Error("second RevokeKey succeeded, want an error")
	}
}

// What a revoked key leaves behind is dropped once it is older than
// RevokedKeyRetention, so the ConfigMap (at most 1 MiB) cannot fill up with
// them and stop new keys being minted.
func TestRevokeKey_DropsRevokedKeysPastRetention(t *testing.T) {
	old := time.Now().Add(-secrets.RevokedKeyRetention - time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	cm := makeConfigMap("my-model", testNamespace)
	cm.Data = map[string]string{
		"user-old-1":    `{"clientId":"user-old-1","creator":"old","revokedAt":"` + old + `"}`,
		"user-recent-1": `{"clientId":"user-recent-1","creator":"recent","revokedAt":"` + recent + `"}`,
		"user-live-1":   `{"clientId":"user-live-1","creator":"live"}`,
	}
	secret := makeSecret("my-model", testNamespace)
	secret.Data = map[string][]byte{"user-live-1": []byte("sk-live")}
	fakeClient := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(secret, cm).Build()
	mgr := secrets.NewManager(fakeClient, testNamespace)

	if err := mgr.RevokeKey(context.Background(), "my-model", "user-live-1"); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	all, err := mgr.ListAllKeysIncludingRevoked(context.Background())
	if err != nil {
		t.Fatalf("ListAllKeysIncludingRevoked: %v", err)
	}
	var ids []string
	for _, k := range all {
		ids = append(ids, k.ClientID)
	}
	slices.Sort(ids)
	if strings.Join(ids, ",") != "user-live-1,user-recent-1" {
		t.Errorf("keys left = %v, want user-live-1 (just revoked) and user-recent-1", ids)
	}
}

// TestCreateKey_ClientIDIsNewAfterItsTombstoneIsDropped: a revoked key's
// tombstone is dropped after RevokedKeyRetention, so nothing in the store
// still names its client ID. A key minted after that must still get a new
// one: usage already recorded under the old ID stays billed to its owner.
func TestCreateKey_ClientIDIsNewAfterItsTombstoneIsDropped(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(makeSecret("model-a", testNamespace), makeConfigMap("model-a", testNamespace)).
		Build()
	ctx := context.Background()
	mgr := secrets.NewManager(fakeClient, testNamespace)
	mint := func() string {
		t.Helper()
		r, err := mgr.CreateKey(ctx, "model-a", secrets.Creator{Username: "alice"}, "test")
		if err != nil {
			t.Fatalf("CreateKey: %v", err)
		}
		if err := mgr.RevokeKey(ctx, "model-a", r.ClientID); err != nil {
			t.Fatalf("RevokeKey %s: %v", r.ClientID, err)
		}
		return r.ClientID
	}

	old := mint()
	// Age the tombstone past retention; the next revoke drops it.
	cm := &corev1.ConfigMap{}
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "model-a-api-key-metadata", Namespace: testNamespace}, cm); err != nil {
		t.Fatal(err)
	}
	var info secrets.KeyInfo
	if err := json.Unmarshal([]byte(cm.Data[old]), &info); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-secrets.RevokedKeyRetention - 24*time.Hour)
	info.RevokedAt = &aged
	b, _ := json.Marshal(info)
	cm.Data[old] = string(b)
	if err := fakeClient.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	second := mint()
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "model-a-api-key-metadata", Namespace: testNamespace}, cm); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.Data[old]; ok {
		t.Fatalf("tombstone for %s not dropped after retention", old)
	}

	if third := mint(); third == old || third == second {
		t.Fatalf("client ID %q issued again after its tombstone was dropped", third)
	}
}
