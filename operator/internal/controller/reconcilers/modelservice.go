// Resource specs based on llm-d-modelservice chart v0.4.7
package reconcilers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

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

	podSpec := corev1.PodSpec{
		ServiceAccountName: saName,
		Containers:         []corev1.Container{container},
		Volumes:            storage.Volumes,
		Tolerations:        model.Spec.Advanced.VLLM.Tolerations,
		NodeSelector:       model.Spec.Advanced.VLLM.NodeSelector,
		Affinity:           model.Spec.Advanced.VLLM.Affinity,
	}

	if storage.InitContainer != nil {
		podSpec.InitContainers = []corev1.Container{*storage.InitContainer}
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   model.Name,
			Labels: labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
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
		VolumeMounts: storage.VolumeMounts,
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

	args := []string{
		"--model", storage.ModelPath,
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
		limits["nvidia.com/gpu"] = *resource.NewQuantity(int64(model.Spec.Resources.GPU.Count), resource.DecimalSI)
	}

	if len(limits) == 0 {
		return nil
	}

	return limits
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
