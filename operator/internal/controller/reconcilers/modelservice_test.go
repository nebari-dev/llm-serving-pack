package reconcilers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

const testHTTPPortName = "http"

func int32Ptr(i int32) *int32 {
	return &i
}

func defaultConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:          "example.com",
		ExternalGatewayName: "external-gw",
		InternalGatewayName: "internal-gw",
		OIDCIssuerURL:       "https://oidc.example.com",
		DefaultServingImage: "ghcr.io/llm-d/llm-d-cuda:v0.5.1",
	}
}

func defaultModel() *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAuthModelName,
			Namespace: "default",
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   "mistralai/Mistral-7B-v0.1",
				Source: llmv1alpha1.ModelSourceHuggingFace,
			},
			Resources: llmv1alpha1.ResourceSpec{
				GPU: llmv1alpha1.GPUSpec{Count: 2, Type: "nvidia"},
			},
			Access: llmv1alpha1.AccessSpec{},
		},
	}
}

func defaultStorage() *StorageResult {
	return &StorageResult{
		ModelPath: modelCachePath,
		Volumes: []corev1.Volume{
			{
				Name: modelStorageVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      modelStorageVolumeName,
				MountPath: modelCachePath,
			},
		},
	}
}

func storageWithInitContainer() *StorageResult {
	s := defaultStorage()
	c := corev1.Container{
		Name:    "model-downloader",
		Image:   "huggingface/transformers:latest",
		Command: []string{"/bin/sh", "-c", "echo downloading"},
	}
	s.InitContainer = &c
	return s
}

func TestBuildModelServiceResources(t *testing.T) { //nolint:gocyclo // table-driven test
	t.Parallel()

	tests := []struct {
		name    string
		model   *llmv1alpha1.LLMModel
		storage *StorageResult
		cfg     *config.OperatorConfig
		check   func(t *testing.T, result *ModelServiceResources, err error)
	}{
		{
			name:    "basic deployment: correct image, name, labels, replicas",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				dep := result.Deployment
				if dep == nil {
					t.Fatal("expected Deployment to be non-nil")
				}
				if dep.Name != testAuthModelName {
					t.Errorf("expected deployment name %q, got %q", testAuthModelName, dep.Name)
				}
				labels := dep.Labels
				if labels["app.kubernetes.io/instance"] != testAuthModelName {
					t.Errorf("expected label app.kubernetes.io/instance=my-model, got %q", labels["app.kubernetes.io/instance"])
				}
				if labels["app.kubernetes.io/managed-by"] != testAuthSAName {
					t.Errorf("expected managed-by label, got %q", labels["app.kubernetes.io/managed-by"])
				}
				// Replicas: nil in spec defaults to 1
				if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
					t.Errorf("expected replicas=1, got %v", dep.Spec.Replicas)
				}
				// Container name is "vllm"
				containers := dep.Spec.Template.Spec.Containers
				if len(containers) != 1 {
					t.Fatalf("expected 1 container, got %d", len(containers))
				}
				if containers[0].Name != "vllm" {
					t.Errorf("expected container name %q, got %q", "vllm", containers[0].Name)
				}
			},
		},
		{
			name: "serving image from model spec when set",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Serving.Image = "custom/vllm:v1.2.3"
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				containers := result.Deployment.Spec.Template.Spec.Containers
				if containers[0].Image != "custom/vllm:v1.2.3" {
					t.Errorf("expected image %q, got %q", "custom/vllm:v1.2.3", containers[0].Image)
				}
			},
		},
		{
			name:    "default image from config when serving.image is empty",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				containers := result.Deployment.Spec.Template.Spec.Containers
				if containers[0].Image != "ghcr.io/llm-d/llm-d-cuda:v0.5.1" {
					t.Errorf("expected default image, got %q", containers[0].Image)
				}
			},
		},
		{
			name: "GPU resources: nvidia.com/gpu in limits when gpu.count > 0",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Resources.GPU = llmv1alpha1.GPUSpec{Count: 4, Type: "nvidia"}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				limits := result.Deployment.Spec.Template.Spec.Containers[0].Resources.Limits
				gpuQ, ok := limits["nvidia.com/gpu"]
				if !ok {
					t.Fatal("expected nvidia.com/gpu in resource limits")
				}
				if gpuQ.Value() != 4 {
					t.Errorf("expected nvidia.com/gpu=4, got %v", gpuQ.Value())
				}
			},
		},
		{
			name: "no GPU resources when gpu.count == 0",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Resources.GPU = llmv1alpha1.GPUSpec{Count: 0, Type: "nvidia"}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				limits := result.Deployment.Spec.Template.Spec.Containers[0].Resources.Limits
				if _, ok := limits["nvidia.com/gpu"]; ok {
					t.Error("expected no nvidia.com/gpu in resource limits when gpu.count == 0")
				}
			},
		},
		{
			name: "tensorParallelism defaults to gpu.count",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Resources.GPU = llmv1alpha1.GPUSpec{Count: 4, Type: "nvidia"}
				// TensorParallelism not set
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.Deployment.Spec.Template.Spec.Containers[0].Args
				assertArgValue(t, args, "--tensor-parallel-size", "4")
			},
		},
		{
			name: "tensorParallelism explicit value used",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Resources.GPU = llmv1alpha1.GPUSpec{Count: 4, Type: "nvidia"}
				m.Spec.Serving.TensorParallelism = int32Ptr(2)
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.Deployment.Spec.Template.Spec.Containers[0].Args
				assertArgValue(t, args, "--tensor-parallel-size", "2")
			},
		},
		{
			name: "vllmArgs passed in container args",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Serving.VLLMArgs = []string{"--max-model-len", "4096"}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.Deployment.Spec.Template.Spec.Containers[0].Args
				assertContainsArgs(t, args, []string{"--max-model-len", "4096"})
			},
		},
		{
			name: "Advanced extraArgs appended after vllmArgs",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Serving.VLLMArgs = []string{"--base-arg"}
				m.Spec.Advanced.VLLM.ExtraArgs = []string{"--extra-arg"}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.Deployment.Spec.Template.Spec.Containers[0].Args
				assertArgOrder(t, args, "--base-arg", "--extra-arg")
			},
		},
		{
			name: "Advanced extraEnv set on container",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Advanced.VLLM.ExtraEnv = []corev1.EnvVar{
					{Name: "MY_VAR", Value: "my-value"},
				}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				envs := result.Deployment.Spec.Template.Spec.Containers[0].Env
				found := false
				for _, e := range envs {
					if e.Name == "MY_VAR" && e.Value == "my-value" {
						found = true
					}
				}
				if !found {
					t.Error("expected MY_VAR=my-value in container env")
				}
			},
		},
		{
			name: "Advanced tolerations on pod spec",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Advanced.VLLM.Tolerations = []corev1.Toleration{
					{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists},
				}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				tolerations := result.Deployment.Spec.Template.Spec.Tolerations
				if len(tolerations) != 1 {
					t.Fatalf("expected 1 toleration, got %d", len(tolerations))
				}
				if tolerations[0].Key != "nvidia.com/gpu" {
					t.Errorf("expected toleration key nvidia.com/gpu, got %q", tolerations[0].Key)
				}
			},
		},
		{
			name: "Advanced nodeSelector on pod spec",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Advanced.VLLM.NodeSelector = map[string]string{"gpu": "true"}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				ns := result.Deployment.Spec.Template.Spec.NodeSelector
				if ns["gpu"] != "true" {
					t.Errorf("expected nodeSelector gpu=true, got %v", ns)
				}
			},
		},
		{
			name: "Advanced affinity on pod spec",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Advanced.VLLM.Affinity = &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"amd64"}},
									},
								},
							},
						},
					},
				}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				affinity := result.Deployment.Spec.Template.Spec.Affinity
				if affinity == nil {
					t.Fatal("expected affinity to be set")
				}
				if affinity.NodeAffinity == nil {
					t.Fatal("expected NodeAffinity to be set")
				}
			},
		},
		{
			name:    "startup probe: GET /v1/models, delay 15, period 30, failure 120",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				container := result.Deployment.Spec.Template.Spec.Containers[0]
				probe := container.StartupProbe
				if probe == nil {
					t.Fatal("expected startup probe to be set")
				}
				if probe.HTTPGet == nil {
					t.Fatal("expected startup probe to use httpGet")
				}
				if probe.HTTPGet.Path != "/v1/models" {
					t.Errorf("expected startup probe path /v1/models, got %q", probe.HTTPGet.Path)
				}
				if probe.InitialDelaySeconds != 15 {
					t.Errorf("expected startup probe initialDelaySeconds=15, got %d", probe.InitialDelaySeconds)
				}
				if probe.PeriodSeconds != 30 {
					t.Errorf("expected startup probe periodSeconds=30, got %d", probe.PeriodSeconds)
				}
				if probe.FailureThreshold != 120 {
					t.Errorf("expected startup probe failureThreshold=120, got %d", probe.FailureThreshold)
				}
			},
		},
		{
			name:    "liveness probe: GET /health, period 10, failure 3",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				container := result.Deployment.Spec.Template.Spec.Containers[0]
				probe := container.LivenessProbe
				if probe == nil {
					t.Fatal("expected liveness probe to be set")
				}
				if probe.HTTPGet == nil {
					t.Fatal("expected liveness probe to use httpGet")
				}
				if probe.HTTPGet.Path != "/health" {
					t.Errorf("expected liveness probe path /health, got %q", probe.HTTPGet.Path)
				}
				if probe.PeriodSeconds != 10 {
					t.Errorf("expected liveness probe periodSeconds=10, got %d", probe.PeriodSeconds)
				}
				if probe.FailureThreshold != 3 {
					t.Errorf("expected liveness probe failureThreshold=3, got %d", probe.FailureThreshold)
				}
			},
		},
		{
			name:    "readiness probe: GET /v1/models, period 5, failure 3",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				container := result.Deployment.Spec.Template.Spec.Containers[0]
				probe := container.ReadinessProbe
				if probe == nil {
					t.Fatal("expected readiness probe to be set")
				}
				if probe.HTTPGet == nil {
					t.Fatal("expected readiness probe to use httpGet")
				}
				if probe.HTTPGet.Path != "/v1/models" {
					t.Errorf("expected readiness probe path /v1/models, got %q", probe.HTTPGet.Path)
				}
				if probe.PeriodSeconds != 5 {
					t.Errorf("expected readiness probe periodSeconds=5, got %d", probe.PeriodSeconds)
				}
				if probe.FailureThreshold != 3 {
					t.Errorf("expected readiness probe failureThreshold=3, got %d", probe.FailureThreshold)
				}
			},
		},
		{
			name:    "init container from StorageResult included",
			model:   defaultModel(),
			storage: storageWithInitContainer(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				initContainers := result.Deployment.Spec.Template.Spec.InitContainers
				if len(initContainers) != 1 {
					t.Fatalf("expected 1 init container, got %d", len(initContainers))
				}
				if initContainers[0].Name != "model-downloader" {
					t.Errorf("expected init container name %q, got %q", "model-downloader", initContainers[0].Name)
				}
			},
		},
		{
			name:    "volumes from StorageResult included",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				volumes := result.Deployment.Spec.Template.Spec.Volumes
				found := false
				for _, v := range volumes {
					if v.Name == modelStorageVolumeName {
						found = true
					}
				}
				if !found {
					t.Error("expected volume 'model-storage' from StorageResult")
				}
				// Also check volume mounts on vllm container
				mounts := result.Deployment.Spec.Template.Spec.Containers[0].VolumeMounts
				foundMount := false
				for _, m := range mounts {
					if m.Name == modelStorageVolumeName && m.MountPath == modelCachePath {
						foundMount = true
					}
				}
				if !foundMount {
					t.Error("expected VolumeMount 'model-storage' at /model-cache")
				}
			},
		},
		{
			name:    "no init container when StorageResult.InitContainer is nil",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				initContainers := result.Deployment.Spec.Template.Spec.InitContainers
				if len(initContainers) != 0 {
					t.Errorf("expected no init containers, got %d", len(initContainers))
				}
			},
		},
		{
			name:    "service: ClusterIP type, port 8000",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				svc := result.Service
				if svc == nil {
					t.Fatal("expected Service to be non-nil")
				}
				if svc.Name != testAuthModelName {
					t.Errorf("expected service name %q, got %q", testAuthModelName, svc.Name)
				}
				if svc.Spec.Type != corev1.ServiceTypeClusterIP {
					t.Errorf("expected ClusterIP service type, got %q", svc.Spec.Type)
				}
				if len(svc.Spec.Ports) != 1 {
					t.Fatalf("expected 1 service port, got %d", len(svc.Spec.Ports))
				}
				if svc.Spec.Ports[0].Port != 8000 {
					t.Errorf("expected port 8000, got %d", svc.Spec.Ports[0].Port)
				}
				if svc.Spec.Ports[0].Name != testHTTPPortName {
					t.Errorf("expected port name 'http', got %q", svc.Spec.Ports[0].Name)
				}
			},
		},
		{
			name:    "serviceAccount: correct name",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				sa := result.ServiceAccount
				if sa == nil {
					t.Fatal("expected ServiceAccount to be non-nil")
				}
				if sa.Name != "my-model-sa" {
					t.Errorf("expected service account name %q, got %q", "my-model-sa", sa.Name)
				}
				// Also check that the deployment references this SA
				saName := result.Deployment.Spec.Template.Spec.ServiceAccountName
				if saName != "my-model-sa" {
					t.Errorf("expected pod serviceAccountName %q, got %q", "my-model-sa", saName)
				}
			},
		},
		{
			name: "PodMonitor created when monitoring.enabled is true",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Serving.Monitoring = llmv1alpha1.MonitoringSpec{Enabled: boolPtr(true)}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PodMonitor == nil {
					t.Fatal("expected PodMonitor to be non-nil when monitoring is enabled")
				}
				pm := result.PodMonitor
				if pm.GetName() != testAuthModelName {
					t.Errorf("expected PodMonitor name %q, got %q", testAuthModelName, pm.GetName())
				}
				if pm.GetAPIVersion() != "monitoring.coreos.com/v1" {
					t.Errorf("expected apiVersion monitoring.coreos.com/v1, got %q", pm.GetAPIVersion())
				}
				if pm.GetKind() != "PodMonitor" {
					t.Errorf("expected kind PodMonitor, got %q", pm.GetKind())
				}
				// Check spec.podMetricsEndpoints
				spec, ok := pm.Object["spec"].(map[string]interface{})
				if !ok {
					t.Fatal("expected PodMonitor spec to be a map")
				}
				endpoints, ok := spec["podMetricsEndpoints"].([]interface{})
				if !ok || len(endpoints) == 0 {
					t.Fatal("expected podMetricsEndpoints to be set")
				}
				ep := endpoints[0].(map[string]interface{})
				if ep["port"] != testHTTPPortName {
					t.Errorf("expected endpoint port 'http', got %v", ep["port"])
				}
				if ep["path"] != "/metrics" {
					t.Errorf("expected endpoint path '/metrics', got %v", ep["path"])
				}
				if ep["interval"] != "30s" {
					t.Errorf("expected endpoint interval '30s', got %v", ep["interval"])
				}
			},
		},
		{
			name:    "PodMonitor NOT created when monitoring.enabled is false",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PodMonitor != nil {
					t.Error("expected PodMonitor to be nil when monitoring is not enabled")
				}
			},
		},
		{
			name: "PodMonitor NOT created when monitoring.enabled is explicitly false",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Serving.Monitoring = llmv1alpha1.MonitoringSpec{Enabled: boolPtr(false)}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.PodMonitor != nil {
					t.Error("expected PodMonitor to be nil when monitoring.enabled=false")
				}
			},
		},
		{
			name: "replicas from model spec used when set",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Serving.Replicas = int32Ptr(3)
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.Deployment.Spec.Replicas == nil || *result.Deployment.Spec.Replicas != 3 {
					t.Errorf("expected replicas=3, got %v", result.Deployment.Spec.Replicas)
				}
			},
		},
		{
			name: "dataParallelism defaults to 1 when not set",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				// DataParallelism not set
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.Deployment.Spec.Template.Spec.Containers[0].Args
				assertArgValue(t, args, "--data-parallel-size", "1")
			},
		},
		{
			name: "dataParallelism explicit value used",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Serving.DataParallelism = int32Ptr(2)
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.Deployment.Spec.Template.Spec.Containers[0].Args
				assertArgValue(t, args, "--data-parallel-size", "2")
			},
		},
		{
			name: "--model arg set to storage.ModelPath",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				return m
			}(),
			storage: &StorageResult{
				ModelPath:    modelCachePath,
				Volumes:      defaultStorage().Volumes,
				VolumeMounts: defaultStorage().VolumeMounts,
			},
			cfg: defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.Deployment.Spec.Template.Spec.Containers[0].Args
				assertArgValue(t, args, "--model", modelCachePath)
			},
		},
		{
			name: "resource limits merged with GPU limit",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Resources.GPU = llmv1alpha1.GPUSpec{Count: 2, Type: "nvidia"}
				m.Spec.Resources.Limits = corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("32Gi"),
				}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				limits := result.Deployment.Spec.Template.Spec.Containers[0].Resources.Limits
				if _, ok := limits[corev1.ResourceCPU]; !ok {
					t.Error("expected CPU in resource limits")
				}
				if _, ok := limits[corev1.ResourceMemory]; !ok {
					t.Error("expected memory in resource limits")
				}
				if _, ok := limits["nvidia.com/gpu"]; !ok {
					t.Error("expected nvidia.com/gpu in resource limits")
				}
			},
		},
		{
			name: "resource requests from model spec",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultModel()
				m.Spec.Resources.Requests = corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				}
				return m
			}(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				requests := result.Deployment.Spec.Template.Spec.Containers[0].Resources.Requests
				if _, ok := requests[corev1.ResourceCPU]; !ok {
					t.Error("expected CPU in resource requests")
				}
			},
		},
		{
			name:    "container port 8000 named http",
			model:   defaultModel(),
			storage: defaultStorage(),
			cfg:     defaultConfig(),
			check: func(t *testing.T, result *ModelServiceResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				ports := result.Deployment.Spec.Template.Spec.Containers[0].Ports
				found := false
				for _, p := range ports {
					if p.ContainerPort == 8000 && p.Name == testHTTPPortName {
						found = true
					}
				}
				if !found {
					t.Error("expected container port 8000 named 'http'")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := BuildModelServiceResources(tt.model, tt.storage, tt.cfg)
			tt.check(t, result, err)
		})
	}
}

// assertArgValue checks that args contains "--flag" followed by "value".
func assertArgValue(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Errorf("expected args to contain %q %q, got %v", flag, value, args)
}

// assertContainsArgs checks that all elements of subset appear in args (in order).
func assertContainsArgs(t *testing.T, args []string, subset []string) {
	t.Helper()
	for _, s := range subset {
		found := false
		for _, a := range args {
			if a == s {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected arg %q in args %v", s, args)
		}
	}
}

// assertArgOrder checks that firstArg appears before secondArg in args.
func assertArgOrder(t *testing.T, args []string, firstArg, secondArg string) {
	t.Helper()
	firstIdx := -1
	secondIdx := -1
	for i, a := range args {
		if a == firstArg {
			firstIdx = i
		}
		if a == secondArg {
			secondIdx = i
		}
	}
	if firstIdx == -1 {
		t.Errorf("expected arg %q in args %v", firstArg, args)
		return
	}
	if secondIdx == -1 {
		t.Errorf("expected arg %q in args %v", secondArg, args)
		return
	}
	if firstIdx >= secondIdx {
		t.Errorf("expected %q to appear before %q in args %v", firstArg, secondArg, args)
	}
}
