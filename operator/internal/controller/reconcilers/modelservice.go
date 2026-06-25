// Resource specs based on llm-d-modelservice chart v0.4.7. NOTE: the
// llm-d-modelservice Helm chart was deprecated as an llm-d component in llm-d
// v0.7.0 (which referenced it at v0.4.9) in favor of a kustomize-based
// install. The chart is off the recommended install path but its repo is NOT
// frozen - it still cuts releases:
// https://github.com/llm-d-incubation/llm-d-modelservice (tags are named
// llm-d-modelservice-vX.Y.Z). The "diff the upstream chart on each release"
// maintenance contract in docs/design.md therefore still has a target; diff
// these specs against the latest llm-d-modelservice-* tag when re-verifying.
// The vLLM serving image is bumped to llm-d-cuda:v0.7.0 (CUDA 13.0.2 runtime).
package reconcilers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

const (
	// modelDownloaderUserGID matches the uid/gid of the `nonroot` user in the
	// distroless base image used for the model-downloader init container
	// (gcr.io/distroless/cc-debian12:nonroot). Setting fsGroup to this value lets
	// kubelet chown PVC-backed model storage to a group the nonroot user can
	// write to.
	modelDownloaderUserGID int64 = 65532

	// nvidiaGPUKey is the NVIDIA GPU identifier, used both as the extended resource
	// the device plugin advertises (in the container's resource limits) and as the
	// taint key NIC applies to GPU node groups (nvidia.com/gpu=true:NoSchedule).
	// Pods that request a GPU must tolerate that taint to schedule onto those nodes.
	nvidiaGPUKey = "nvidia.com/gpu"
)

// shmVolumeName is the memory-backed emptyDir mounted at /dev/shm in the vllm
// container. Kubernetes gives pods a 64Mi /dev/shm by default, which is far
// too small for vLLM: with tensor parallelism > 1 the worker processes
// exchange tensors through POSIX shared memory (and NCCL's SHM transport uses
// it too), so a default-sized /dev/shm makes multi-GPU models hang or crash
// during engine startup.
const shmVolumeName = "shm"

const shmMountPath = "/dev/shm"

// ModelServiceResources holds the Kubernetes resources for serving an LLMModel.
type ModelServiceResources struct {
	Deployment     *appsv1.Deployment
	Service        *corev1.Service
	ServiceAccount *corev1.ServiceAccount
	// PodMonitor is nil if monitoring is disabled; unstructured since PodMonitor CRD may not be installed
	PodMonitor *unstructured.Unstructured
}

// BuildModelServiceResources is a pure function that computes the Kubernetes resources
// needed to serve an LLMModel, based on its spec, storage results, and operator config.
func BuildModelServiceResources(model *llmv1alpha1.LLMModel, storage *StorageResult, cfg *config.OperatorConfig) (*ModelServiceResources, error) {
	labels := StandardLabels(model)
	saName := model.Name + "-sa"

	deployment, err := buildDeployment(model, storage, cfg, labels, saName)
	if err != nil {
		return nil, fmt.Errorf("building deployment: %w", err)
	}

	svc := buildService(model, labels)
	sa := buildServiceAccount(saName, labels)
	podMonitor := buildPodMonitor(model, labels)

	return &ModelServiceResources{
		Deployment:     deployment,
		Service:        svc,
		ServiceAccount: sa,
		PodMonitor:     podMonitor,
	}, nil
}

func buildDeployment(
	model *llmv1alpha1.LLMModel,
	storage *StorageResult,
	cfg *config.OperatorConfig,
	labels map[string]string,
	saName string,
) (*appsv1.Deployment, error) { //nolint:unparam // error return kept for consistency with other build functions
	replicas := int32(1)
	if model.Spec.Serving.Replicas != nil {
		replicas = *model.Spec.Serving.Replicas
	}

	container := buildVLLMContainer(model, storage, cfg)

	volumes := make([]corev1.Volume, 0, len(storage.Volumes)+1)
	volumes = append(volumes, storage.Volumes...)
	volumes = append(volumes, buildShmVolume(model))

	fsGroupChangePolicy := corev1.FSGroupChangeOnRootMismatch
	podSpec := corev1.PodSpec{
		ServiceAccountName: saName,
		Containers:         []corev1.Container{container},
		Volumes:            volumes,
		Tolerations:        buildTolerations(model),
		NodeSelector:       model.Spec.Advanced.VLLM.NodeSelector,
		Affinity:           model.Spec.Advanced.VLLM.Affinity,
		SecurityContext: &corev1.PodSecurityContext{
			FSGroup:             ptr.To(modelDownloaderUserGID),
			FSGroupChangePolicy: &fsGroupChangePolicy,
		},
	}

	if storage.InitContainer != nil {
		podSpec.InitContainers = []corev1.Container{*storage.InitContainer}
	}

	// Default to Recreate, not RollingUpdate: the old pod holds the node's
	// GPUs (and a ReadWriteOnce model PVC), so a surged replacement pod can
	// never schedule on a cluster without spare GPU capacity and the rollout
	// deadlocks until someone deletes the old ReplicaSet by hand. Recreate
	// tears the old pod down first, accepting brief downtime over a wedged
	// rollout. Clusters with free GPUs can opt back into RollingUpdate via
	// spec.serving.updateStrategy.
	strategyType := appsv1.RecreateDeploymentStrategyType
	if model.Spec.Serving.UpdateStrategy == llmv1alpha1.UpdateStrategyRollingUpdate {
		strategyType = appsv1.RollingUpdateDeploymentStrategyType
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   model.Name,
			Labels: labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: strategyType,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": model.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}

	return dep, nil
}

// buildShmVolume returns the memory-backed emptyDir backing the vllm
// container's /dev/shm. When the model declares a memory limit, the volume is
// capped at that limit; tmpfs pages count toward the container's memory
// limit anyway, so the cap reserves nothing extra while keeping the kubelet's
// enforcement (evict on overrun) predictable. Without a memory limit the
// volume is left uncapped, like docker run --shm-size on a host.
func buildShmVolume(model *llmv1alpha1.LLMModel) corev1.Volume {
	emptyDir := &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}
	if mem, ok := model.Spec.Resources.Limits[corev1.ResourceMemory]; ok {
		emptyDir.SizeLimit = ptr.To(mem)
	}
	return corev1.Volume{
		Name:         shmVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: emptyDir},
	}
}

func buildVLLMContainer(
	model *llmv1alpha1.LLMModel,
	storage *StorageResult,
	cfg *config.OperatorConfig,
) corev1.Container {
	image := cfg.DefaultServingImage
	if model.Spec.Serving.Image != "" {
		image = model.Spec.Serving.Image
	}

	args := buildVLLMArgs(model, storage)
	limits := buildResourceLimits(model)

	mounts := make([]corev1.VolumeMount, 0, len(storage.VolumeMounts)+1)
	mounts = append(mounts, storage.VolumeMounts...)
	mounts = append(mounts, corev1.VolumeMount{Name: shmVolumeName, MountPath: shmMountPath})

	port8000 := intstr.FromString("http")

	container := corev1.Container{
		Name:  "vllm",
		Image: image,
		Args:  args,
		Ports: []corev1.ContainerPort{
			{Name: "http", ContainerPort: 8000, Protocol: corev1.ProtocolTCP},
		},
		Resources: corev1.ResourceRequirements{
			Limits:   limits,
			Requests: model.Spec.Resources.Requests,
		},
		Env:          model.Spec.Advanced.VLLM.ExtraEnv,
		VolumeMounts: mounts,
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/v1/models",
					Port: port8000,
				},
			},
			InitialDelaySeconds: 15,
			PeriodSeconds:       30,
			FailureThreshold:    120,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: port8000,
				},
			},
			PeriodSeconds:    10,
			FailureThreshold: 3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/v1/models",
					Port: port8000,
				},
			},
			PeriodSeconds:    5,
			FailureThreshold: 3,
		},
	}

	return container
}

func buildVLLMArgs(model *llmv1alpha1.LLMModel, storage *StorageResult) []string {
	tensorParallelism := model.Spec.Resources.GPU.Count
	if model.Spec.Serving.TensorParallelism != nil {
		tensorParallelism = *model.Spec.Serving.TensorParallelism
	}

	dataParallelism := int32(1)
	if model.Spec.Serving.DataParallelism != nil {
		dataParallelism = *model.Spec.Serving.DataParallelism
	}

	// --served-model-name makes vLLM accept the model under its public
	// name (spec.model.name, e.g. "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4") even
	// though --model points at a local path. Without this, OpenAI-style
	// clients that pass "model": "<name>" in the request body get a
	// vLLM 404 "The model X does not exist" because vLLM has registered
	// the model under the path instead.
	args := []string{
		"--model", storage.ModelPath,
		"--served-model-name", model.Spec.Model.Name,
		"--tensor-parallel-size", fmt.Sprintf("%d", tensorParallelism),
		"--data-parallel-size", fmt.Sprintf("%d", dataParallelism),
	}

	args = append(args, model.Spec.Serving.VLLMArgs...)
	args = append(args, model.Spec.Advanced.VLLM.ExtraArgs...)

	return args
}

func buildResourceLimits(model *llmv1alpha1.LLMModel) corev1.ResourceList {
	limits := make(corev1.ResourceList)

	// Copy existing limits from spec
	for k, v := range model.Spec.Resources.Limits {
		limits[k] = v
	}

	// Add GPU limit if count > 0
	if model.Spec.Resources.GPU.Count > 0 {
		limits[nvidiaGPUKey] = *resource.NewQuantity(int64(model.Spec.Resources.GPU.Count), resource.DecimalSI)
	}

	if len(limits) == 0 {
		return nil
	}

	return limits
}

// buildTolerations returns the pod tolerations for a model. User-provided
// tolerations from model.Spec.Advanced.VLLM.Tolerations are always preserved.
// When the model requests a GPU, a toleration for the nvidia.com/gpu taint is injected
// automatically so the pod can land on GPU nodes that NIC taints nvidia.com/gpu=true:NoSchedule.
// The injected toleration uses operator: Exists, which matches any taint value.
func buildTolerations(model *llmv1alpha1.LLMModel) []corev1.Toleration {
	// Copy to avoid mutating the model spec's underlying slice.
	tolerations := append([]corev1.Toleration(nil), model.Spec.Advanced.VLLM.Tolerations...)

	// Only inject for GPU-requesting workloads, and only if the user hasn't
	// already specified a toleration for the GPU taint.
	if model.Spec.Resources.GPU.Count > 0 && !tolerationsGPUTaint(tolerations) {
		tolerations = append(tolerations, corev1.Toleration{
			Key:      nvidiaGPUKey,
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		})
	}

	return tolerations
}

// tolerationsGPUTaint reports whether the given tolerations already cover the
// nvidia.com/gpu taint, either via an explicit key match or an empty-key
// (tolerate-everything) toleration.
func tolerationsGPUTaint(tolerations []corev1.Toleration) bool {
	for _, t := range tolerations {
		if t.Key == nvidiaGPUKey || (t.Key == "" && t.Operator == corev1.TolerationOpExists) {
			return true
		}
	}
	return false
}

func buildService(model *llmv1alpha1.LLMModel, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   model.Name,
			Labels: labels,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app.kubernetes.io/instance": model.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       8000,
					TargetPort: intstr.FromString("http"),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

func buildServiceAccount(saName string, labels map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:   saName,
			Labels: labels,
		},
	}
}

func buildPodMonitor(model *llmv1alpha1.LLMModel, labels map[string]string) *unstructured.Unstructured {
	if model.Spec.Serving.Monitoring.Enabled == nil || !*model.Spec.Serving.Monitoring.Enabled {
		return nil
	}

	matchLabels := map[string]interface{}{
		"app.kubernetes.io/instance": model.Name,
	}

	pm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "PodMonitor",
			"metadata": map[string]interface{}{
				"name":   model.Name,
				"labels": labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": matchLabels,
				},
				"podMetricsEndpoints": []interface{}{
					map[string]interface{}{
						"port":     "http",
						"path":     "/metrics",
						"interval": "30s",
					},
				},
			},
		},
	}

	return pm
}

// labelsToInterface converts a map[string]string to map[string]interface{} for use in unstructured objects.
func labelsToInterface(labels map[string]string) map[string]interface{} {
	m := make(map[string]interface{}, len(labels))
	for k, v := range labels {
		m[k] = v
	}
	return m
}
