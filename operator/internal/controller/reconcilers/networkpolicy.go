package reconcilers

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

// BuildNetworkPolicy returns a NetworkPolicy for the model pods that enforces
// default-deny ingress, allowing traffic only from the Envoy Gateway namespaces,
// EPP pods in the same namespace, and Prometheus in the monitoring namespace.
func BuildNetworkPolicy(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) *networkingv1.NetworkPolicy {
	labels := StandardLabels(model)

	ingressRules := buildNetworkPolicyIngressRules(cfg)

	policyTypeIngress := networkingv1.PolicyTypeIngress

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      model.Name + "-deny-direct",
			Namespace: model.Namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": model.Name,
					"llm.nebari.dev/model":       model.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{policyTypeIngress},
			Ingress:     ingressRules,
		},
	}
}

func buildNetworkPolicyIngressRules(cfg *config.OperatorConfig) []networkingv1.NetworkPolicyIngressRule {
	var rules []networkingv1.NetworkPolicyIngressRule

	// Allow from gateway namespaces. Deduplicate if both gateways are in the same namespace.
	gatewayNamespaces := uniqueStrings(cfg.ExternalGatewayNS, cfg.InternalGatewayNS)
	for _, ns := range gatewayNamespaces {
		rules = append(rules, networkingv1.NetworkPolicyIngressRule{
			From: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": ns,
						},
					},
				},
			},
		})
	}

	// Allow from EPP pods in same namespace (no namespaceSelector means same namespace).
	rules = append(rules, networkingv1.NetworkPolicyIngressRule{
		From: []networkingv1.NetworkPolicyPeer{
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/name": "epp",
					},
				},
			},
		},
	})

	// Allow from monitoring namespace on port 8000 only.
	tcpProto := corev1.ProtocolTCP
	port8000 := intstr.FromInt32(8000)
	rules = append(rules, networkingv1.NetworkPolicyIngressRule{
		From: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "monitoring",
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: &tcpProto,
				Port:     &port8000,
			},
		},
	})

	return rules
}

// uniqueStrings returns a slice of unique strings preserving order,
// omitting empty strings and duplicates.
func uniqueStrings(values ...string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}
