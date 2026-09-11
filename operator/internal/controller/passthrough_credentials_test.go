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
	"errors"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

type listErrorReader struct {
	client.Reader
	err error
}

func (r *listErrorReader) List(
	context.Context,
	client.ObjectList,
	...client.ListOption,
) error {
	return r.err
}

func passthroughModelWithCredential(name, namespace, secretName string) *llmv1alpha1.PassthroughModel {
	return &llmv1alpha1.PassthroughModel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: llmv1alpha1.PassthroughModelSpec{
			Provider: llmv1alpha1.ProviderSpec{CredentialSecretName: secretName},
		},
	}
}

func TestIndexPassthroughModelByUpstreamCredentialSecret(t *testing.T) {
	tests := []struct {
		name string
		obj  client.Object
		want []string
	}{
		{
			name: "indexes the referenced Secret",
			obj:  passthroughModelWithCredential("openrouter", "llm", "openrouter-credential"),
			want: []string{"openrouter-credential"},
		},
		{
			name: "ignores an empty Secret reference",
			obj:  passthroughModelWithCredential("openrouter", "llm", ""),
		},
		{
			name: "ignores other object kinds",
			obj:  namedSecret("openrouter-credential", "llm"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indexPassthroughModelByUpstreamCredentialSecret(tt.obj)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("index values %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnqueuePassthroughModelsForUpstreamCredentialSecret(t *testing.T) {
	objects := []client.Object{
		passthroughModelWithCredential("openrouter-primary", "llm", "openrouter-credential"),
		passthroughModelWithCredential("openrouter-fallback", "llm", "openrouter-credential"),
		passthroughModelWithCredential("different-provider", "llm", "other-credential"),
		passthroughModelWithCredential("same-secret-other-namespace", "other", "openrouter-credential"),
	}
	c := fake.NewClientBuilder().
		WithScheme(apikeysTestScheme(t)).
		WithObjects(objects...).
		WithIndex(
			&llmv1alpha1.PassthroughModel{},
			upstreamCredentialSecretIndex,
			indexPassthroughModelByUpstreamCredentialSecret,
		).
		Build()

	requests := enqueuePassthroughModelsForUpstreamCredentialSecret(
		context.Background(),
		c,
		namedSecret("openrouter-credential", "llm"),
	)

	want := map[types.NamespacedName]bool{
		{Name: "openrouter-primary", Namespace: "llm"}:  true,
		{Name: "openrouter-fallback", Namespace: "llm"}: true,
	}
	if len(requests) != len(want) {
		t.Fatalf("got %d requests (%v), want %d", len(requests), requests, len(want))
	}
	for _, request := range requests {
		if !want[request.NamespacedName] {
			t.Errorf("unexpected reconcile request for %s", request.NamespacedName)
			continue
		}
		delete(want, request.NamespacedName)
	}
	for missing := range want {
		t.Errorf("missing reconcile request for %s", missing)
	}
}

func TestEnqueuePassthroughModelsForUnreferencedUpstreamCredentialSecret(t *testing.T) {
	model := passthroughModelWithCredential("openrouter", "llm", "openrouter-credential")
	c := fake.NewClientBuilder().
		WithScheme(apikeysTestScheme(t)).
		WithObjects(model).
		WithIndex(
			&llmv1alpha1.PassthroughModel{},
			upstreamCredentialSecretIndex,
			indexPassthroughModelByUpstreamCredentialSecret,
		).
		Build()

	requests := enqueuePassthroughModelsForUpstreamCredentialSecret(
		context.Background(),
		c,
		namedSecret("unrelated", "llm"),
	)

	if len(requests) != 0 {
		t.Fatalf("got requests %v for an unreferenced Secret", requests)
	}
}

func TestEnqueuePassthroughModelsForUpstreamCredentialSecretListFailure(t *testing.T) {
	c := &listErrorReader{err: errors.New("temporary cache list failure")}

	requests := enqueuePassthroughModelsForUpstreamCredentialSecret(
		context.Background(),
		c,
		namedSecret("openrouter-credential", "llm"),
	)

	if requests != nil {
		t.Fatalf("got requests %v when the indexed List failed", requests)
	}
}

func TestUpstreamCredentialSecretInNamespacePredicate(t *testing.T) {
	tests := []struct {
		name              string
		operatorNamespace string
		secretNamespace   string
		want              bool
	}{
		{
			name:              "accepts a Secret in the operator namespace",
			operatorNamespace: "llm",
			secretNamespace:   "llm",
			want:              true,
		},
		{
			name:              "rejects a Secret outside the operator namespace",
			operatorNamespace: "llm",
			secretNamespace:   "other",
			want:              false,
		},
		{
			name:            "accepts any namespace in test mode",
			secretNamespace: "other",
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := upstreamCredentialSecretInNamespacePredicate(tt.operatorNamespace)
			got := filter.Create(event.CreateEvent{Object: namedSecret("credential", tt.secretNamespace)})
			if got != tt.want {
				t.Fatalf("predicate returned %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPassthroughPhaseForConditions(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		want       llmv1alpha1.PassthroughModelPhase
	}{
		{
			name: "an unresolved credential does not block readiness",
			conditions: []metav1.Condition{{
				Type:   CondUpstreamCredentialResolved,
				Status: metav1.ConditionFalse,
				Reason: "SecretNotFound",
			}},
			want: llmv1alpha1.PassthroughPhaseReady,
		},
		{
			name: "a credential lookup failure does not block readiness",
			conditions: []metav1.Condition{{
				Type:   CondUpstreamCredentialResolved,
				Status: metav1.ConditionUnknown,
				Reason: "LookupFailed",
			}},
			want: llmv1alpha1.PassthroughPhaseReady,
		},
		{
			name: "a gateway apply failure sets the error phase",
			conditions: []metav1.Condition{{
				Type:   CondBackendConfigured,
				Status: metav1.ConditionFalse,
				Reason: "ApplyFailed",
			}},
			want: llmv1alpha1.PassthroughPhaseError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := passthroughPhaseForConditions(tt.conditions); got != tt.want {
				t.Fatalf("got phase %q, want %q", got, tt.want)
			}
		})
	}
}
