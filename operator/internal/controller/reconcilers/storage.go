package reconcilers

import (
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

const (
	modelStorageVolumeName = "model-storage"
	modelCachePath         = "/model-cache"

	defaultModelDownloaderImage = "ghcr.io/nebari-dev/llm-serving-pack/model-downloader:latest"
)

// modelDownloaderImage returns the image to use for the model-downloader init
// container. It can be overridden via the LLM_MODEL_DOWNLOADER_IMAGE env var;
// otherwise it falls back to defaultModelDownloaderImage.
func modelDownloaderImage() string {
	if v := os.Getenv("LLM_MODEL_DOWNLOADER_IMAGE"); v != "" {
		return v
	}
	return defaultModelDownloaderImage
}

// StorageResult holds the storage-related Kubernetes resources for an LLMModel.
type StorageResult struct {
	// PVC is the PersistentVolumeClaim to create, or nil if not needed.
	PVC *corev1.PersistentVolumeClaim
	// InitContainer is the init container to add to the pod, or nil if none is needed.
	InitContainer *corev1.Container
	// Volumes are the volumes to add to the pod spec.
	Volumes []corev1.Volume
	// VolumeMounts are the volume mounts for the vLLM container.
	VolumeMounts []corev1.VolumeMount
	// ModelPath is the path where the model is available inside the vLLM container.
	ModelPath string
}

// BuildStorageSpec is a pure function that takes an LLMModel and returns the storage
// resources needed: PVC, init container, volumes, and volume mounts.
// defaultStorageClassName is applied to PVCs when the model doesn't specify one.
func BuildStorageSpec(model *llmv1alpha1.LLMModel, defaultStorageClassName string) (*StorageResult, error) {
	switch model.Spec.Model.Source {
	case llmv1alpha1.ModelSourceOCI:
		return buildOCIStorageSpec(model)
	default:
		// Default is HuggingFace
		return buildHFStorageSpec(model, defaultStorageClassName)
	}
}

func buildHFStorageSpec(model *llmv1alpha1.LLMModel, defaultStorageClassName string) (*StorageResult, error) {
	result := &StorageResult{
		ModelPath: modelCachePath,
	}

	storage := model.Spec.Model.Storage

	// Build volume based on storage type
	switch storage.Type {
	case llmv1alpha1.StorageTypeEmptyDir:
		sizeLimit, err := parseStorageSize(storage.Size)
		if err != nil {
			return nil, fmt.Errorf("parsing storage size: %w", err)
		}
		result.Volumes = []corev1.Volume{
			{
				Name: modelStorageVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						SizeLimit: &sizeLimit,
					},
				},
			},
		}
	default:
		// Default to PVC
		pvcName := model.Name + "-model-storage"
		pvc, err := buildPVC(pvcName, storage, defaultStorageClassName)
		if err != nil {
			return nil, fmt.Errorf("building PVC: %w", err)
		}
		result.PVC = pvc
		result.Volumes = []corev1.Volume{
			{
				Name: modelStorageVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			},
		}
	}

	result.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      modelStorageVolumeName,
			MountPath: modelCachePath,
		},
	}

	// Build init container unless preload is explicitly false
	if model.Spec.Model.Preload == nil || *model.Spec.Model.Preload {
		initContainer := buildHFInitContainer(model)
		result.InitContainer = &initContainer
	}

	return result, nil
}

func buildPVC(name string, storage llmv1alpha1.StorageSpec, defaultStorageClassName string) (*corev1.PersistentVolumeClaim, error) {
	size, err := parseStorageSize(storage.Size)
	if err != nil {
		return nil, fmt.Errorf("parsing storage size: %w", err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: size,
				},
			},
		},
	}

	sc := storage.StorageClassName
	if sc == "" {
		sc = defaultStorageClassName
	}
	if sc != "" {
		pvc.Spec.StorageClassName = &sc
	}

	return pvc, nil
}

func buildHFInitContainer(model *llmv1alpha1.LLMModel) corev1.Container {
	args := []string{model.Spec.Model.Name, "--local-dir", modelCachePath}
	if model.Spec.Model.Revision != "" {
		args = append(args, "--revision", model.Spec.Model.Revision)
	}

	container := corev1.Container{
		Name:  "model-downloader",
		Image: modelDownloaderImage(),
		Args:  args,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      modelStorageVolumeName,
				MountPath: modelCachePath,
			},
		},
	}

	if model.Spec.Model.AuthSecretName != "" {
		container.Env = []corev1.EnvVar{
			{
				Name: "HF_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: model.Spec.Model.AuthSecretName,
						},
						Key: "HF_TOKEN",
					},
				},
			},
		}
	}

	return container
}

func buildOCIStorageSpec(model *llmv1alpha1.LLMModel) (*StorageResult, error) {
	result := &StorageResult{
		ModelPath: modelCachePath,
	}

	result.Volumes = []corev1.Volume{
		{
			Name: modelStorageVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	result.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      modelStorageVolumeName,
			MountPath: modelCachePath,
		},
	}

	initContainer := corev1.Container{
		Name:    "model-loader",
		Image:   model.Spec.Model.Image,
		Command: []string{"cp", "-r", "/models/.", "/shared-models/"},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      modelStorageVolumeName,
				MountPath: "/shared-models",
			},
		},
	}
	result.InitContainer = &initContainer

	return result, nil
}

func parseStorageSize(size string) (resource.Quantity, error) {
	if size == "" {
		return resource.MustParse("0"), nil
	}
	q, err := resource.ParseQuantity(size)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("invalid storage size %q: %w", size, err)
	}
	return q, nil
}
