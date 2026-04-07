package reconcilers

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func boolPtr(b bool) *bool {
	return &b
}

func makeHFModel(name string, storage llmv1alpha1.StorageSpec) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:    "mistralai/Devstral-Small-2505",
				Source:  llmv1alpha1.ModelSourceHuggingFace,
				Storage: storage,
			},
			Resources: llmv1alpha1.ResourceSpec{
				GPU: llmv1alpha1.GPUSpec{Count: 1, Type: "nvidia"},
			},
			Access: llmv1alpha1.AccessSpec{},
		},
	}
}

func TestBuildStorageSpec(t *testing.T) { //nolint:gocyclo // table-driven test
	t.Parallel()

	tests := []struct {
		name  string
		model *llmv1alpha1.LLMModel
		check func(t *testing.T, result *StorageResult, err error)
	}{
		{
			name: "HF + PVC returns PVC with correct name and size",
			model: makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypePVC,
				Size: "200Gi",
			}),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PVC == nil {
					t.Fatal("expected PVC to be non-nil")
				}
				if result.PVC.Name != "devstral-32b-model-storage" {
					t.Errorf("expected PVC name %q, got %q", "devstral-32b-model-storage", result.PVC.Name)
				}
				expectedSize := resource.MustParse("200Gi")
				actualSize := result.PVC.Spec.Resources.Requests[corev1.ResourceStorage]
				if actualSize.Cmp(expectedSize) != 0 {
					t.Errorf("expected PVC size %v, got %v", expectedSize, actualSize)
				}
				// Check volume references PVC
				found := false
				for _, v := range result.Volumes {
					if v.Name == modelStorageVolumeName && v.PersistentVolumeClaim != nil {
						if v.PersistentVolumeClaim.ClaimName == "devstral-32b-model-storage" {
							found = true
						}
					}
				}
				if !found {
					t.Error("expected volume 'model-storage' referencing the PVC")
				}
				// Check VolumeMount at /model-cache
				foundMount := false
				for _, m := range result.VolumeMounts {
					if m.Name == modelStorageVolumeName && m.MountPath == modelCachePath {
						foundMount = true
					}
				}
				if !foundMount {
					t.Error("expected VolumeMount 'model-storage' at /model-cache")
				}
				// Check init container exists with lock script
				if result.InitContainer == nil {
					t.Fatal("expected init container to be non-nil")
				}
				script := strings.Join(result.InitContainer.Command[2:], " ")
				if !strings.Contains(script, "huggingface-cli download") {
					t.Error("expected init container script to contain 'huggingface-cli download'")
				}
			},
		},
		{
			name: "HF + PVC + custom storageClassName",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
					Type:             llmv1alpha1.StorageTypePVC,
					Size:             "200Gi",
					StorageClassName: "fast-ssd",
				})
				return m
			}(),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PVC == nil {
					t.Fatal("expected PVC to be non-nil")
				}
				if result.PVC.Spec.StorageClassName == nil || *result.PVC.Spec.StorageClassName != "fast-ssd" {
					t.Errorf("expected storageClassName 'fast-ssd', got %v", result.PVC.Spec.StorageClassName)
				}
			},
		},
		{
			name: "HF + emptyDir no PVC, emptyDir volume with sizeLimit",
			model: makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypeEmptyDir,
				Size: "200Gi",
			}),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PVC != nil {
					t.Error("expected PVC to be nil for emptyDir storage")
				}
				found := false
				for _, v := range result.Volumes {
					if v.Name == modelStorageVolumeName && v.EmptyDir != nil {
						expectedSize := resource.MustParse("200Gi")
						if v.EmptyDir.SizeLimit != nil &&
							v.EmptyDir.SizeLimit.Cmp(expectedSize) == 0 {
							found = true
						}
					}
				}
				if !found {
					t.Error("expected emptyDir volume 'model-storage' with sizeLimit 200Gi")
				}
				// Init container should still be present
				if result.InitContainer == nil {
					t.Error("expected init container for emptyDir HF model")
				}
			},
		},
		{
			name: "HF + authSecretName sets HF_TOKEN env var",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
					Type: llmv1alpha1.StorageTypePVC,
					Size: "200Gi",
				})
				m.Spec.Model.AuthSecretName = "my-hf-secret"
				return m
			}(),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer == nil {
					t.Fatal("expected init container to be non-nil")
				}
				found := false
				for _, env := range result.InitContainer.Env {
					if env.Name == "HF_TOKEN" &&
						env.ValueFrom != nil &&
						env.ValueFrom.SecretKeyRef != nil &&
						env.ValueFrom.SecretKeyRef.Name == "my-hf-secret" {
						found = true
					}
				}
				if !found {
					t.Error("expected HF_TOKEN env var sourced from secret 'my-hf-secret'")
				}
			},
		},
		{
			name: "HF without authSecretName has no HF_TOKEN env var",
			model: makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypePVC,
				Size: "200Gi",
			}),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer == nil {
					t.Fatal("expected init container to be non-nil")
				}
				for _, env := range result.InitContainer.Env {
					if env.Name == "HF_TOKEN" {
						t.Error("expected no HF_TOKEN env var when no authSecretName")
					}
				}
			},
		},
		{
			name: "HF + revision includes --revision in script",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
					Type: llmv1alpha1.StorageTypePVC,
					Size: "200Gi",
				})
				m.Spec.Model.Revision = "abc123def456"
				return m
			}(),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer == nil {
					t.Fatal("expected init container to be non-nil")
				}
				script := strings.Join(result.InitContainer.Command[2:], " ")
				if !strings.Contains(script, "--revision abc123def456") {
					t.Errorf("expected script to contain '--revision abc123def456', got: %s", script)
				}
			},
		},
		{
			name: "HF without revision has no --revision in script",
			model: makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypePVC,
				Size: "200Gi",
			}),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer == nil {
					t.Fatal("expected init container to be non-nil")
				}
				script := strings.Join(result.InitContainer.Command[2:], " ")
				if strings.Contains(script, "--revision") {
					t.Errorf("expected no --revision in script, got: %s", script)
				}
			},
		},
		{
			name: "HF + preload=false has volumes but no init container",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
					Type: llmv1alpha1.StorageTypePVC,
					Size: "200Gi",
				})
				m.Spec.Model.Preload = boolPtr(false)
				return m
			}(),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer != nil {
					t.Error("expected no init container when preload=false")
				}
				if len(result.Volumes) == 0 {
					t.Error("expected volumes to be present even when preload=false")
				}
				if len(result.VolumeMounts) == 0 {
					t.Error("expected volume mounts to be present even when preload=false")
				}
			},
		},
		{
			name: "HF + preload=nil defaults to creating init container",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
					Type: llmv1alpha1.StorageTypePVC,
					Size: "200Gi",
				})
				// Preload is nil by default
				return m
			}(),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer == nil {
					t.Error("expected init container when preload=nil (default true)")
				}
			},
		},
		{
			name: "OCI source no PVC, emptyDir, init container with cp command",
			model: func() *llmv1alpha1.LLMModel {
				return &llmv1alpha1.LLMModel{
					ObjectMeta: metav1.ObjectMeta{Name: "my-oci-model"},
					Spec: llmv1alpha1.LLMModelSpec{
						Model: llmv1alpha1.ModelSpec{
							Name:   "my-org/my-model",
							Source: llmv1alpha1.ModelSourceOCI,
							Image:  "registry.example.com/models/my-model:latest",
						},
						Resources: llmv1alpha1.ResourceSpec{
							GPU: llmv1alpha1.GPUSpec{Count: 1, Type: "nvidia"},
						},
						Access: llmv1alpha1.AccessSpec{},
					},
				}
			}(),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PVC != nil {
					t.Error("expected no PVC for OCI source")
				}
				// Check emptyDir volume exists
				foundVolume := false
				for _, v := range result.Volumes {
					if v.Name == modelStorageVolumeName && v.EmptyDir != nil {
						foundVolume = true
					}
				}
				if !foundVolume {
					t.Error("expected emptyDir volume 'model-storage' for OCI source")
				}
				// Check init container with cp command
				if result.InitContainer == nil {
					t.Fatal("expected init container for OCI source")
				}
				if len(result.InitContainer.Command) < 3 {
					t.Fatalf("expected at least 3 command args, got %v", result.InitContainer.Command)
				}
				if result.InitContainer.Command[0] != "cp" {
					t.Errorf("expected init container command to start with 'cp', got %v", result.InitContainer.Command)
				}
				// Check init container mounts at /shared-models
				foundInitMount := false
				for _, m := range result.InitContainer.VolumeMounts {
					if m.Name == modelStorageVolumeName && m.MountPath == "/shared-models" {
						foundInitMount = true
					}
				}
				if !foundInitMount {
					t.Error("expected init container to mount 'model-storage' at /shared-models")
				}
				// Check vLLM container mounts at /model-cache
				foundVLLMMount := false
				for _, m := range result.VolumeMounts {
					if m.Name == modelStorageVolumeName && m.MountPath == modelCachePath {
						foundVLLMMount = true
					}
				}
				if !foundVLLMMount {
					t.Error("expected vLLM VolumeMount 'model-storage' at /model-cache")
				}
			},
		},
		{
			name: "OCI source init container image matches model.image",
			model: func() *llmv1alpha1.LLMModel {
				return &llmv1alpha1.LLMModel{
					ObjectMeta: metav1.ObjectMeta{Name: "my-oci-model"},
					Spec: llmv1alpha1.LLMModelSpec{
						Model: llmv1alpha1.ModelSpec{
							Name:   "my-org/my-model",
							Source: llmv1alpha1.ModelSourceOCI,
							Image:  "registry.example.com/models/my-model:v2",
						},
						Resources: llmv1alpha1.ResourceSpec{
							GPU: llmv1alpha1.GPUSpec{Count: 1, Type: "nvidia"},
						},
						Access: llmv1alpha1.AccessSpec{},
					},
				}
			}(),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer == nil {
					t.Fatal("expected init container for OCI source")
				}
				if result.InitContainer.Image != "registry.example.com/models/my-model:v2" {
					t.Errorf("expected init container image %q, got %q",
						"registry.example.com/models/my-model:v2", result.InitContainer.Image)
				}
			},
		},
		{
			name: "ModelPath is always /model-cache",
			model: makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypePVC,
				Size: "200Gi",
			}),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ModelPath != modelCachePath {
					t.Errorf("expected ModelPath '/model-cache', got %q", result.ModelPath)
				}
			},
		},
		{
			name: "init container lock script contains noclobber set -C",
			model: makeHFModel("devstral-32b", llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypePVC,
				Size: "200Gi",
			}),
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InitContainer == nil {
					t.Fatal("expected init container to be non-nil")
				}
				script := strings.Join(result.InitContainer.Command[2:], " ")
				if !strings.Contains(script, "set -C") {
					t.Errorf("expected lock script to contain 'set -C' (noclobber), got: %s", script)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := BuildStorageSpec(tt.model)
			tt.check(t, result, err)
		})
	}
}
