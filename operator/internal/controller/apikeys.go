/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// This file holds the api-keys Secret plumbing shared by the LLMModel and
// PassthroughModel controllers. Both render external SecurityPolicies from
// the same fleet-wide inputs (pooled credentialRefs across every model's
// api-keys Secret, a per-model client-ID allow-list), so both register the
// same Secret watch and gather the same inputs during Reconcile.

// managedByOperatorPredicate filters watch events to objects the operator
// itself manages, keeping the Secret watch from firing on unrelated Secrets.
func managedByOperatorPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetLabels()["app.kubernetes.io/managed-by"] == "nebari-llm-operator"
	})
}

// apiKeyClientIDs reads a model's api-keys Secret and returns its client IDs
// (data-key names). A missing Secret (first reconcile, before it is created)
// yields no client IDs, which renders a deny-all external authorization until
// a key is minted.
func apiKeyClientIDs(ctx context.Context, c client.Reader, modelName, namespace string) ([]string, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: reconcilers.APIKeySecretName(modelName), Namespace: namespace}
	if err := c.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return reconcilers.ClientIDsFromSecret(secret), nil
}

// apiKeySecretNamesWith returns the names of all operator-managed api-keys
// Secrets in the namespace (every model's Secret), guaranteeing ownName is
// present, sorted for deterministic rendering. These pool into each external
// SecurityPolicy's apiKeyAuth.credentialRefs so any valid key authenticates on
// the shared listener; the per-model authorization block then scopes it (#116).
func apiKeySecretNamesWith(ctx context.Context, c client.Reader, namespace, ownName string) ([]string, error) {
	var list corev1.SecretList
	if err := c.List(ctx, &list, client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/managed-by": "nebari-llm-operator"}); err != nil {
		return nil, err
	}
	set := map[string]struct{}{ownName: {}}
	for i := range list.Items {
		if strings.HasSuffix(list.Items[i].Name, reconcilers.APIKeySecretSuffix) {
			set[list.Items[i].Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// enqueueAllModelsForAPIKeySecret enqueues every item of list (an
// LLMModelList or PassthroughModelList) in the Secret's namespace when an
// operator-managed api-keys Secret changes. Both the per-model authorization
// allow-list (a model's own keys) and the pooled credentialRefs (every
// model's Secret) depend on the full set of api-keys Secrets, so a key
// mint/revoke or a model being added/removed must re-reconcile every model.
// Churn is bounded by model count; scoping key-mint updates to the owning
// model is a possible follow-up.
func enqueueAllModelsForAPIKeySecret(ctx context.Context, c client.Reader, obj client.Object, list client.ObjectList) []reconcile.Request {
	if !strings.HasSuffix(obj.GetName(), reconcilers.APIKeySecretSuffix) {
		return nil
	}
	if err := c.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "listing models for api-keys Secret change; re-render skipped until the next event",
			"secret", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		logf.FromContext(ctx).Error(err, "extracting model list for api-keys Secret change",
			"secret", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(items))
	for _, item := range items {
		model, ok := item.(client.Object)
		if !ok {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Name: model.GetName(), Namespace: model.GetNamespace(),
		}})
	}
	return reqs
}
