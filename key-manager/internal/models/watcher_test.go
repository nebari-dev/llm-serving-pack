package models_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/key-manager/internal/models"
)

func boolPtr(b bool) *bool { return &b }

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := llmv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func makeLLMModel(name, namespace, modelName string, public *bool, groups []string) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   modelName,
				Source: llmv1alpha1.ModelSourceHuggingFace,
			},
			Resources: llmv1alpha1.ResourceSpec{
				GPU: llmv1alpha1.GPUSpec{Count: 1, Type: "nvidia"},
			},
			Access: llmv1alpha1.AccessSpec{
				Public: public,
				Groups: groups,
			},
		},
	}
}

func TestWatcher_Sync(t *testing.T) {
	scheme := buildScheme(t)

	model := makeLLMModel("gpt-model", "default", "openai/gpt-4", boolPtr(false), []string{"data-team"})
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model).Build()

	w := models.NewWatcher(fakeClient)
	if err := w.Sync(context.Background()); err != nil {
		t.Fatalf("Sync error: %v", err)
	}

	listed := w.ListModels()
	if len(listed) != 1 {
		t.Fatalf("expected 1 model, got %d", len(listed))
	}
	got := listed[0]
	if got.Name != "gpt-model" {
		t.Errorf("expected Name=gpt-model, got %s", got.Name)
	}
	if got.Namespace != "default" {
		t.Errorf("expected Namespace=default, got %s", got.Namespace)
	}
	if got.ModelName != "openai/gpt-4" {
		t.Errorf("expected ModelName=openai/gpt-4, got %s", got.ModelName)
	}
}

func TestWatcher_ListModels(t *testing.T) {
	scheme := buildScheme(t)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeLLMModel("model-a", "default", "org/model-a", boolPtr(false), []string{"team-a"}),
		makeLLMModel("model-b", "default", "org/model-b", boolPtr(true), nil),
	).Build()

	w := models.NewWatcher(fakeClient)
	if err := w.Sync(context.Background()); err != nil {
		t.Fatalf("Sync error: %v", err)
	}

	listed := w.ListModels()
	if len(listed) != 2 {
		t.Fatalf("expected 2 models, got %d", len(listed))
	}
}

func TestWatcher_FilterModelsForUser(t *testing.T) {
	tests := []struct {
		name       string
		llmModels  []*llmv1alpha1.LLMModel
		userGroups []string
		wantNames  []string
	}{
		{
			name: "matching group returns model",
			llmModels: []*llmv1alpha1.LLMModel{
				makeLLMModel("private-model", "default", "org/private", boolPtr(false), []string{"data-team"}),
			},
			userGroups: []string{"data-team"},
			wantNames:  []string{"private-model"},
		},
		{
			name: "no matching groups returns empty",
			llmModels: []*llmv1alpha1.LLMModel{
				makeLLMModel("private-model", "default", "org/private", boolPtr(false), []string{"data-team"}),
			},
			userGroups: []string{"other-team"},
			wantNames:  []string{},
		},
		{
			name: "public model always returned regardless of groups",
			llmModels: []*llmv1alpha1.LLMModel{
				makeLLMModel("public-model", "default", "org/public", boolPtr(true), nil),
			},
			userGroups: []string{"some-random-group"},
			wantNames:  []string{"public-model"},
		},
		{
			name: "empty user groups only returns public models",
			llmModels: []*llmv1alpha1.LLMModel{
				makeLLMModel("public-model", "default", "org/public", boolPtr(true), nil),
				makeLLMModel("private-model", "default", "org/private", boolPtr(false), []string{"data-team"}),
			},
			userGroups: []string{},
			wantNames:  []string{"public-model"},
		},
		{
			name: "multiple models returns correct subset",
			llmModels: []*llmv1alpha1.LLMModel{
				makeLLMModel("public-model", "default", "org/public", boolPtr(true), nil),
				makeLLMModel("team-a-model", "default", "org/a", boolPtr(false), []string{"team-a"}),
				makeLLMModel("team-b-model", "default", "org/b", boolPtr(false), []string{"team-b"}),
				makeLLMModel("shared-model", "default", "org/shared", boolPtr(false), []string{"team-a", "team-b"}),
			},
			userGroups: []string{"team-a"},
			wantNames:  []string{"public-model", "team-a-model", "shared-model"},
		},
		{
			name: "nil public field treated as non-public",
			llmModels: []*llmv1alpha1.LLMModel{
				makeLLMModel("nil-public-model", "default", "org/nil", nil, []string{"data-team"}),
			},
			userGroups: []string{},
			wantNames:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)

			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, m := range tc.llmModels {
				builder = builder.WithObjects(m)
			}
			fakeClient := builder.Build()

			w := models.NewWatcher(fakeClient)
			if err := w.Sync(context.Background()); err != nil {
				t.Fatalf("Sync error: %v", err)
			}

			got := w.FilterModelsForUser(tc.userGroups)

			gotNames := make(map[string]bool)
			for _, m := range got {
				gotNames[m.Name] = true
			}

			if len(got) != len(tc.wantNames) {
				t.Errorf("expected %d models, got %d: %v", len(tc.wantNames), len(got), gotNames)
				return
			}

			for _, wantName := range tc.wantNames {
				if !gotNames[wantName] {
					t.Errorf("expected model %q in result, got: %v", wantName, gotNames)
				}
			}
		})
	}
}
