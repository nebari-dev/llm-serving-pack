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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func apikeysTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := llmv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func namedSecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

func llmModel(name, namespace string) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

func passthroughModel(name, namespace string) *llmv1alpha1.PassthroughModel {
	return &llmv1alpha1.PassthroughModel{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

func TestEnqueueAllModelsForAPIKeySecret(t *testing.T) {
	tests := []struct {
		name      string
		secret    *corev1.Secret
		list      client.ObjectList
		objects   []client.Object
		wantNames []string
	}{
		{
			name:   "api-keys Secret enqueues every LLMModel in its namespace",
			secret: namedSecret("phi-api-keys", "llm"),
			list:   &llmv1alpha1.LLMModelList{},
			objects: []client.Object{
				llmModel("phi", "llm"),
				llmModel("mistral", "llm"),
				llmModel("elsewhere", "other-ns"),
			},
			wantNames: []string{"phi", "mistral"},
		},
		{
			name:   "non api-keys Secret enqueues nothing",
			secret: namedSecret("phi-tls", "llm"),
			list:   &llmv1alpha1.LLMModelList{},
			objects: []client.Object{
				llmModel("phi", "llm"),
			},
			wantNames: nil,
		},
		{
			name:   "api-keys Secret enqueues every PassthroughModel in its namespace",
			secret: namedSecret("claude-api-keys", "llm"),
			list:   &llmv1alpha1.PassthroughModelList{},
			objects: []client.Object{
				passthroughModel("claude", "llm"),
				passthroughModel("gemini", "llm"),
			},
			wantNames: []string{"claude", "gemini"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(apikeysTestScheme(t)).
				WithObjects(tt.objects...).
				Build()

			reqs := enqueueAllModelsForAPIKeySecret(context.Background(), c, tt.secret, tt.list)

			got := map[string]bool{}
			for _, r := range reqs {
				if r.Namespace != tt.secret.Namespace {
					t.Errorf("request %q enqueued in namespace %q, want %q", r.Name, r.Namespace, tt.secret.Namespace)
				}
				got[r.Name] = true
			}
			if len(reqs) != len(tt.wantNames) {
				t.Fatalf("got %d requests (%v), want %d (%v)", len(reqs), got, len(tt.wantNames), tt.wantNames)
			}
			for _, want := range tt.wantNames {
				if !got[want] {
					t.Errorf("missing request for %q (got %v)", want, got)
				}
			}
		})
	}
}
