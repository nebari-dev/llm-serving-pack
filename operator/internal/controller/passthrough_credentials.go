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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// This file handles the status-only upstream credential probe and its
// targeted Secret watch. Credential values never feed rendered resources.

const (
	// The index key mirrors the API field path used to select affected models.
	upstreamCredentialSecretIndex = "spec.provider.credentialSecretName"
	upstreamCredentialAPIKey      = "apiKey"
)

// resolveUpstreamCredential reports whether the provider Secret has a non-empty API key.
func resolveUpstreamCredential(
	ctx context.Context,
	c client.Reader,
	pm *llmv1alpha1.PassthroughModel,
) metav1.Condition {
	secretName := pm.Spec.Provider.CredentialSecretName
	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: pm.Namespace}, secret)
	if apierrors.IsNotFound(err) {
		return newUpstreamCredentialCondition(
			metav1.ConditionFalse,
			"SecretNotFound",
			fmt.Sprintf("spec.provider.credentialSecretName %q was not found", secretName),
		)
	}
	if err != nil {
		return newUpstreamCredentialCondition(
			metav1.ConditionUnknown,
			"LookupFailed",
			fmt.Sprintf("failed to read spec.provider.credentialSecretName %q: %v", secretName, err),
		)
	}

	if len(secret.Data[upstreamCredentialAPIKey]) == 0 {
		return newUpstreamCredentialCondition(
			metav1.ConditionFalse,
			"APIKeyMissing",
			fmt.Sprintf("Secret %q has no non-empty %q entry", secretName, upstreamCredentialAPIKey),
		)
	}

	return newUpstreamCredentialCondition(
		metav1.ConditionTrue,
		"Resolved",
		fmt.Sprintf("Secret %q has a non-empty %q entry", secretName, upstreamCredentialAPIKey),
	)
}

// newUpstreamCredentialCondition constructs the status-only credential condition.
func newUpstreamCredentialCondition(status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:    CondUpstreamCredentialResolved,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

// indexPassthroughModelByUpstreamCredentialSecret indexes the referenced Secret name.
func indexPassthroughModelByUpstreamCredentialSecret(obj client.Object) []string {
	pm, ok := obj.(*llmv1alpha1.PassthroughModel)
	if !ok || pm.Spec.Provider.CredentialSecretName == "" {
		return nil
	}
	return []string{pm.Spec.Provider.CredentialSecretName}
}

// enqueuePassthroughModelsForUpstreamCredentialSecret returns models in the
// Secret's namespace that reference its name.
func enqueuePassthroughModelsForUpstreamCredentialSecret(
	ctx context.Context,
	c client.Reader,
	obj client.Object,
) []reconcile.Request {
	var models llmv1alpha1.PassthroughModelList
	if err := c.List(
		ctx,
		&models,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{upstreamCredentialSecretIndex: obj.GetName()},
	); err != nil {
		logf.FromContext(ctx).Error(
			err,
			"listing PassthroughModels for credential Secret change",
			"secret", obj.GetName(),
			"namespace", obj.GetNamespace(),
		)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(models.Items))
	for i := range models.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      models.Items[i].Name,
			Namespace: models.Items[i].Namespace,
		}})
	}
	return requests
}

// upstreamCredentialSecretInNamespacePredicate limits events to the operator namespace.
func upstreamCredentialSecretInNamespacePredicate(namespace string) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return namespace == "" || obj.GetNamespace() == namespace
	})
}
