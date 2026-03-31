package audit_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/audit"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/models"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/secrets"
)

// mockModelLister is a test double for ModelLister.
type mockModelLister struct {
	models []models.ModelInfo
}

func (m *mockModelLister) ListModels() []models.ModelInfo {
	return m.models
}

// mockKeyManager is a test double for KeyManager.
type mockKeyManager struct {
	mu          sync.Mutex
	keys        map[string][]secrets.KeyInfo // modelName -> keys
	revokedKeys []revokedKey
	revokeErr   error
	listErr     error
}

type revokedKey struct {
	modelName string
	clientID  string
}

func (m *mockKeyManager) ListKeys(ctx context.Context, modelName string) ([]secrets.KeyInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keys[modelName], nil
}

func (m *mockKeyManager) RevokeKey(ctx context.Context, modelName, clientID string) error {
	if m.revokeErr != nil {
		return m.revokeErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokedKeys = append(m.revokedKeys, revokedKey{modelName: modelName, clientID: clientID})
	return nil
}

func (m *mockKeyManager) getRevokedKeys() []revokedKey {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]revokedKey, len(m.revokedKeys))
	copy(result, m.revokedKeys)
	return result
}

// newTestLogger returns a discard logger for tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestAuditor_RunOnce(t *testing.T) {
	tests := []struct {
		name          string
		modelList     []models.ModelInfo
		keysByModel   map[string][]secrets.KeyInfo
		userGroups    map[string][]string // username -> groups
		lookupErr     map[string]error    // username -> error to return
		revokeErr     error
		wantRevoked   []revokedKey
		wantErrSubstr string
	}{
		{
			name: "user still has group access: key not revoked",
			modelList: []models.ModelInfo{
				{Name: "llama", Public: false, Groups: []string{"ml-team", "research"}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"llama": {
					{ClientID: "user-alice-1", Creator: "alice", ModelName: "llama"},
				},
			},
			userGroups:  map[string][]string{"alice": {"ml-team", "devs"}},
			wantRevoked: nil,
		},
		{
			name: "user lost group access: key revoked",
			modelList: []models.ModelInfo{
				{Name: "llama", Public: false, Groups: []string{"ml-team"}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"llama": {
					{ClientID: "user-alice-1", Creator: "alice", ModelName: "llama"},
				},
			},
			userGroups:  map[string][]string{"alice": {"devs"}},
			wantRevoked: []revokedKey{{modelName: "llama", clientID: "user-alice-1"}},
		},
		{
			name: "public model: keys never revoked by audit",
			modelList: []models.ModelInfo{
				{Name: "public-gpt", Public: true, Groups: []string{}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"public-gpt": {
					{ClientID: "user-bob-1", Creator: "bob", ModelName: "public-gpt"},
				},
			},
			userGroups:  map[string][]string{"bob": {}},
			wantRevoked: nil,
		},
		{
			name: "multiple models: correct keys revoked from correct models",
			modelList: []models.ModelInfo{
				{Name: "model-a", Public: false, Groups: []string{"team-a"}},
				{Name: "model-b", Public: false, Groups: []string{"team-b"}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"model-a": {
					{ClientID: "user-alice-1", Creator: "alice", ModelName: "model-a"}, // alice lost access
					{ClientID: "user-carol-1", Creator: "carol", ModelName: "model-a"}, // carol still has access
				},
				"model-b": {
					{ClientID: "user-bob-1", Creator: "bob", ModelName: "model-b"}, // bob lost access
				},
			},
			userGroups: map[string][]string{
				"alice": {"team-c"},         // lost team-a
				"carol": {"team-a", "devs"}, // still has team-a
				"bob":   {},                 // lost team-b
			},
			wantRevoked: []revokedKey{
				{modelName: "model-a", clientID: "user-alice-1"},
				{modelName: "model-b", clientID: "user-bob-1"},
			},
		},
		{
			name: "UserGroupsLookup error: key not revoked (fail-safe)",
			modelList: []models.ModelInfo{
				{Name: "secure-model", Public: false, Groups: []string{"vip"}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"secure-model": {
					{ClientID: "user-alice-1", Creator: "alice", ModelName: "secure-model"},
				},
			},
			userGroups:  nil,
			lookupErr:   map[string]error{"alice": errors.New("OIDC unreachable")},
			wantRevoked: nil,
		},
		{
			name: "RevokeKey error: continues auditing other keys, returns error",
			modelList: []models.ModelInfo{
				{Name: "model-x", Public: false, Groups: []string{"team-x"}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"model-x": {
					{ClientID: "user-alice-1", Creator: "alice", ModelName: "model-x"},
					{ClientID: "user-bob-1", Creator: "bob", ModelName: "model-x"},
				},
			},
			userGroups:    map[string][]string{"alice": {}, "bob": {}},
			revokeErr:     errors.New("k8s conflict"),
			wantRevoked:   nil, // revokeErr is set so nothing will be recorded by mock
			wantErrSubstr: "k8s conflict",
		},
		{
			name: "no keys for a model: no-op, no errors",
			modelList: []models.ModelInfo{
				{Name: "empty-model", Public: false, Groups: []string{"some-group"}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"empty-model": {},
			},
			userGroups:  map[string][]string{},
			wantRevoked: nil,
		},
		{
			name: "user has one matching group out of many model groups: key not revoked",
			modelList: []models.ModelInfo{
				{Name: "shared-model", Public: false, Groups: []string{"team-a", "team-b", "team-c"}},
			},
			keysByModel: map[string][]secrets.KeyInfo{
				"shared-model": {
					{ClientID: "user-dave-1", Creator: "dave", ModelName: "shared-model"},
				},
			},
			userGroups:  map[string][]string{"dave": {"team-c"}}, // matches team-c only
			wantRevoked: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lister := &mockModelLister{models: tc.modelList}
			km := &mockKeyManager{
				keys:      tc.keysByModel,
				revokeErr: tc.revokeErr,
			}

			lookup := func(ctx context.Context, username string) ([]string, error) {
				if tc.lookupErr != nil {
					if err, ok := tc.lookupErr[username]; ok {
						return nil, err
					}
				}
				if tc.userGroups == nil {
					return nil, nil
				}
				return tc.userGroups[username], nil
			}

			a := audit.NewAuditor(lister, km, lookup, time.Minute, newTestLogger())
			err := a.RunOnce(context.Background())

			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstr)
				}
				if !containsStr(err.Error(), tc.wantErrSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSubstr)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			gotRevoked := km.getRevokedKeys()
			if !revokedSetsEqual(tc.wantRevoked, gotRevoked) {
				t.Errorf("revoked keys mismatch\nwant: %v\ngot:  %v", tc.wantRevoked, gotRevoked)
			}
		})
	}
}

// containsStr is a simple substring check.
func containsStr(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}())
}

// revokedSetsEqual checks that two slices of revokedKey contain the same entries
// regardless of order (since map iteration order is non-deterministic).
func revokedSetsEqual(want, got []revokedKey) bool {
	if len(want) != len(got) {
		return false
	}
	counts := make(map[revokedKey]int)
	for _, k := range want {
		counts[k]++
	}
	for _, k := range got {
		counts[k]--
		if counts[k] < 0 {
			return false
		}
	}
	return true
}
