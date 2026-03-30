package models

import (
	"context"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// ModelInfo is a simplified view of an LLMModel for the key manager.
type ModelInfo struct {
	Name      string
	Namespace string
	ModelName string   // spec.model.name
	Public    bool     // spec.access.public
	Groups    []string // spec.access.groups
}

// Watcher watches LLMModel CRs and maintains an in-memory cache.
type Watcher struct {
	client client.Client
	mu     sync.RWMutex
	models map[string]ModelInfo // key: "namespace/name"
}

// NewWatcher creates a new Watcher using the given controller-runtime client.
func NewWatcher(c client.Client) *Watcher {
	return &Watcher{
		client: c,
		models: make(map[string]ModelInfo),
	}
}

// Sync refreshes the in-memory cache by listing all LLMModels across all namespaces.
func (w *Watcher) Sync(ctx context.Context) error {
	list := &llmv1alpha1.LLMModelList{}
	if err := w.client.List(ctx, list); err != nil {
		return err
	}

	updated := make(map[string]ModelInfo, len(list.Items))
	for _, m := range list.Items {
		key := m.Namespace + "/" + m.Name

		public := false
		if m.Spec.Access.Public != nil {
			public = *m.Spec.Access.Public
		}

		groups := make([]string, len(m.Spec.Access.Groups))
		copy(groups, m.Spec.Access.Groups)

		updated[key] = ModelInfo{
			Name:      m.Name,
			Namespace: m.Namespace,
			ModelName: m.Spec.Model.Name,
			Public:    public,
			Groups:    groups,
		}
	}

	w.mu.Lock()
	w.models = updated
	w.mu.Unlock()

	return nil
}

// ListModels returns all cached models.
func (w *Watcher) ListModels() []ModelInfo {
	w.mu.RLock()
	defer w.mu.RUnlock()

	result := make([]ModelInfo, 0, len(w.models))
	for _, m := range w.models {
		result = append(result, m)
	}
	return result
}

// FilterModelsForUser returns models accessible by a user with the given groups.
// A model is accessible if:
// - model.Public is true, OR
// - any of the user's groups matches any of the model's groups.
func (w *Watcher) FilterModelsForUser(groups []string) []ModelInfo {
	w.mu.RLock()
	defer w.mu.RUnlock()

	userGroups := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		userGroups[g] = struct{}{}
	}

	var result []ModelInfo
	for _, m := range w.models {
		if m.Public {
			result = append(result, m)
			continue
		}
		for _, g := range m.Groups {
			if _, ok := userGroups[g]; ok {
				result = append(result, m)
				break
			}
		}
	}
	return result
}
