package models

import (
	"context"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// ModelInfo is a simplified view of an LLMModel or PassthroughModel for
// the key manager. Both kinds share the same api-keys Secret naming and
// access semantics, so the rest of the key-manager treats them uniformly.
type ModelInfo struct {
	Name        string
	Namespace   string
	ModelName   string   // LLMModel: spec.model.name; PassthroughModel: provider hostname
	Public      bool     // spec.access.public
	Groups      []string // spec.access.groups
	Passthrough bool     // true when backed by a PassthroughModel CR
	Provider    string   // provider hostname, set for passthrough entries
}

// Watcher watches LLMModel and PassthroughModel CRs and maintains an
// in-memory cache.
type Watcher struct {
	client client.Client
	mu     sync.RWMutex
	models map[string]ModelInfo // key: "<kind>/namespace/name"
}

// NewWatcher creates a new Watcher using the given controller-runtime client.
func NewWatcher(c client.Client) *Watcher {
	return &Watcher{
		client: c,
		models: make(map[string]ModelInfo),
	}
}

// Sync refreshes the in-memory cache by listing all LLMModels and
// PassthroughModels across all namespaces. Cache keys are kind-prefixed so
// an LLMModel and a PassthroughModel sharing a name (discouraged: their
// api-keys Secrets would collide) cannot silently evict each other.
func (w *Watcher) Sync(ctx context.Context) error {
	list := &llmv1alpha1.LLMModelList{}
	if err := w.client.List(ctx, list); err != nil {
		return err
	}

	ptList := &llmv1alpha1.PassthroughModelList{}
	if err := w.client.List(ctx, ptList); err != nil {
		return err
	}

	updated := make(map[string]ModelInfo, len(list.Items)+len(ptList.Items))
	for _, m := range list.Items {
		key := "llmmodel/" + m.Namespace + "/" + m.Name

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

	for _, pm := range ptList.Items {
		key := "passthroughmodel/" + pm.Namespace + "/" + pm.Name

		public := false
		if pm.Spec.Access.Public != nil {
			public = *pm.Spec.Access.Public
		}

		groups := make([]string, len(pm.Spec.Access.Groups))
		copy(groups, pm.Spec.Access.Groups)

		updated[key] = ModelInfo{
			Name:        pm.Name,
			Namespace:   pm.Namespace,
			ModelName:   pm.Spec.Provider.Hostname,
			Public:      public,
			Groups:      groups,
			Passthrough: true,
			Provider:    pm.Spec.Provider.Hostname,
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
