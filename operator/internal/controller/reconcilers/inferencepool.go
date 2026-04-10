// Resource specs based on GIE inferencepool chart v1.4.0
package reconcilers

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

const (
	eppImage = "ghcr.io/llm-d/llm-d-inference-scheduler:v0.6.1-rc.1"
)

// InferencePoolResources holds the Kubernetes resources for the Gateway API Inference Extension.
type InferencePoolResources struct {
	InferencePool     *unstructured.Unstructured // inference.networking.k8s.io/v1 InferencePool
	EPPDeployment     *appsv1.Deployment
	EPPService        *corev1.Service
	EPPServiceAccount *corev1.ServiceAccount
	EPPRole           *rbacv1.Role
	EPPRoleBinding    *rbacv1.RoleBinding
}

// BuildInferencePoolResources is a pure function that computes the Kubernetes resources
// needed for intelligent inference scheduling using the Gateway API Inference Extension.
func BuildInferencePoolResources(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) (*InferencePoolResources, error) {
	labels := StandardLabels(model)
	eppName := model.Name + "-epp"

	inferencePool := buildInferencePool(model, labels)
	eppDeployment := buildEPPDeployment(model, eppName, labels)
	eppService := buildEPPService(model, eppName, labels)
	eppSA := buildEPPServiceAccount(eppName, labels)
	eppRole := buildEPPRole(eppName, labels)
	eppRoleBinding := buildEPPRoleBinding(eppName, labels)

	return &InferencePoolResources{
		InferencePool:     inferencePool,
		EPPDeployment:     eppDeployment,
		EPPService:        eppService,
		EPPServiceAccount: eppSA,
		EPPRole:           eppRole,
		EPPRoleBinding:    eppRoleBinding,
	}, nil
}

func buildInferencePool(model *llmv1alpha1.LLMModel, labels map[string]string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "inference.networking.k8s.io/v1",
			"kind":       "InferencePool",
			"metadata": map[string]interface{}{
				"name":   model.Name,
				"labels": labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"targetPorts": []interface{}{
					map[string]interface{}{
						"number": int64(8000),
					},
				},
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app.kubernetes.io/instance": model.Name,
					},
				},
				"endpointPickerRef": map[string]interface{}{
					"name": model.Name + "-epp",
					"kind": "Service",
					"port": map[string]interface{}{
						"number": int64(9002),
					},
				},
			},
		},
	}
}

func buildEPPDeployment(model *llmv1alpha1.LLMModel, eppName string, labels map[string]string) *appsv1.Deployment {
	replicas := int32(1)

	// EPP-specific labels extend the standard labels with app.kubernetes.io/name: epp
	eppLabels := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		eppLabels[k] = v
	}
	eppLabels["app.kubernetes.io/name"] = "epp"

	container := corev1.Container{
		Name:  "epp",
		Image: eppImage,
		Ports: []corev1.ContainerPort{
			{Name: "grpc", ContainerPort: 9002, Protocol: corev1.ProtocolTCP},
			{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
		},
		Args: []string{
			"--pool-name", model.Name,
			"--pool-namespace", model.Namespace,
			"--grpc-port", "9002",
			"--metrics-port", "9090",
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: eppLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": model.Name,
					"app.kubernetes.io/name":     "epp",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: eppLabels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: eppName,
					Containers:         []corev1.Container{container},
				},
			},
		},
	}
}

func buildEPPService(model *llmv1alpha1.LLMModel, eppName string, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app.kubernetes.io/instance": model.Name,
				"app.kubernetes.io/name":     "epp",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       9002,
					TargetPort: intstr.FromString("grpc"),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "metrics",
					Port:       9090,
					TargetPort: intstr.FromString("metrics"),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

func buildEPPServiceAccount(eppName string, labels map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
	}
}

func buildEPPRole(eppName string, labels map[string]string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"inference.networking.k8s.io"},
				Resources: []string{"inferencepools"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"endpoints", "services"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

func buildEPPRoleBinding(eppName string, labels map[string]string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     eppName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "ServiceAccount",
				Name: eppName,
			},
		},
	}
}
