package reconcilers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/llm-serving-pack/operator/api/v1alpha1"
)

func boolPtr(b bool) *bool {
	return &b
}

func makeHFModel(storage llmv1alpha1.StorageSpec) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "devstral-32b"},
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

func TestModelDownloaderImage(t *testing.T) {
	const def = "ghcr.io/nebari-dev/llm-serving-pack/model-downloader:latest"
	tests := []struct{ name, env, want string }{
		{"default when unset", "", def},
		{"override from env", "quay.io/nebari/llm-serving-pack-model-downloader:sha-abc1234", "quay.io/nebari/llm-serving-pack-model-downloader:sha-abc1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LLM_MODEL_DOWNLOADER_IMAGE", tt.env)
			if got := modelDownloaderImage(); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestBuildStorageSpec(t *testing.T) { //nolint:gocyclo // table-driven test
	t.Parallel()

	tests := []struct {
		name                    string
		model                   *llmv1alpha1.LLMModel
		defaultStorageClassName string
		check                   func(t *testing.T, result *StorageResult, err error)
	}{
		{
			name: "HF + PVC returns PVC with correct name and size",
			model: makeHFModel(llmv1alpha1.StorageSpec{
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
				// Check init container exists with correct args
				if result.InitContainer == nil {
					t.Fatal("expected init container to be non-nil")
				}
				args := result.InitContainer.Args
				if len(args) < 3 || args[0] != "mistralai/Devstral-Small-2505" || args[1] != "--local-dir" || args[2] != "/model-cache" {
					t.Errorf("expected args [model --local-dir /model-cache], got %v", args)
				}
			},
		},
		{
			name: "HF + PVC + custom storageClassName",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel(llmv1alpha1.StorageSpec{
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
			model: makeHFModel(llmv1alpha1.StorageSpec{
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
				m := makeHFModel(llmv1alpha1.StorageSpec{
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
			model: makeHFModel(llmv1alpha1.StorageSpec{
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
			name: "HF + revision includes --revision in args",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel(llmv1alpha1.StorageSpec{
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
				args := result.InitContainer.Args
				foundRevision := false
				for i, a := range args {
					if a == "--revision" && i+1 < len(args) && args[i+1] == "abc123def456" {
						foundRevision = true
					}
				}
				if !foundRevision {
					t.Errorf("expected args to contain '--revision abc123def456', got %v", args)
				}
			},
		},
		{
			name: "HF without revision has no --revision in args",
			model: makeHFModel(llmv1alpha1.StorageSpec{
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
				for _, a := range result.InitContainer.Args {
					if a == "--revision" {
						t.Errorf("expected no --revision in args, got %v", result.InitContainer.Args)
					}
				}
			},
		},
		{
			name: "HF + preload=false has volumes but no init container",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel(llmv1alpha1.StorageSpec{
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
				m := makeHFModel(llmv1alpha1.StorageSpec{
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
			model: makeHFModel(llmv1alpha1.StorageSpec{
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
			name: "PVC uses default storageClassName when model does not specify one",
			model: makeHFModel(llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypePVC,
				Size: "200Gi",
			}),
			defaultStorageClassName: "ebs-gp3",
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PVC == nil {
					t.Fatal("expected PVC to be non-nil")
				}
				if result.PVC.Spec.StorageClassName == nil || *result.PVC.Spec.StorageClassName != "ebs-gp3" {
					t.Errorf("expected storageClassName 'ebs-gp3', got %v", result.PVC.Spec.StorageClassName)
				}
			},
		},
		{
			name: "model storageClassName overrides default",
			model: func() *llmv1alpha1.LLMModel {
				m := makeHFModel(llmv1alpha1.StorageSpec{
					Type:             llmv1alpha1.StorageTypePVC,
					Size:             "200Gi",
					StorageClassName: "efs-sc",
				})
				return m
			}(),
			defaultStorageClassName: "ebs-gp3",
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PVC == nil {
					t.Fatal("expected PVC to be non-nil")
				}
				if result.PVC.Spec.StorageClassName == nil || *result.PVC.Spec.StorageClassName != "efs-sc" {
					t.Errorf("expected storageClassName 'efs-sc', got %v", result.PVC.Spec.StorageClassName)
				}
			},
		},
		{
			name: "no storageClassName set when both model and default are empty",
			model: makeHFModel(llmv1alpha1.StorageSpec{
				Type: llmv1alpha1.StorageTypePVC,
				Size: "200Gi",
			}),
			defaultStorageClassName: "",
			check: func(t *testing.T, result *StorageResult, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PVC == nil {
					t.Fatal("expected PVC to be non-nil")
				}
				if result.PVC.Spec.StorageClassName != nil {
					t.Errorf("expected no storageClassName, got %v", *result.PVC.Spec.StorageClassName)
				}
			},
		},
		{
			name: "init container uses args not shell script",
			model: makeHFModel(llmv1alpha1.StorageSpec{
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
				if len(result.InitContainer.Command) != 0 {
					t.Errorf("expected no Command (uses entrypoint from image), got %v", result.InitContainer.Command)
				}
				if len(result.InitContainer.Args) == 0 {
					t.Error("expected Args to be set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := BuildStorageSpec(tt.model, tt.defaultStorageClassName)
			tt.check(t, result, err)
		})
	}
}
