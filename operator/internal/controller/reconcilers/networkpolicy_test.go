package reconcilers

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

func defaultNetworkPolicyConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:          "example.com",
		ExternalGatewayName: "external-gw",
		ExternalGatewayNS:   "envoy-gateway-system",
		InternalGatewayName: "internal-gw",
		InternalGatewayNS:   "envoy-gateway-system",
		OIDCIssuerURL:       "https://oidc.example.com",
		DefaultServingImage: "ghcr.io/llm-d/llm-d-cuda:v0.5.1",
	}
}

func defaultNetworkPolicyModel() *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAuthModelName,
			Namespace: "model-ns",
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   "mistralai/Mistral-7B-v0.1",
				Source: llmv1alpha1.ModelSourceHuggingFace,
			},
			Resources: llmv1alpha1.ResourceSpec{
				GPU: llmv1alpha1.GPUSpec{Count: 1, Type: "nvidia"},
			},
		},
	}
}

// hasNamespaceRule checks whether the policy's ingress rules contain a namespaceSelector
// matching the given namespace name label value.
func hasNamespaceRule(ingress []networkingv1.NetworkPolicyIngressRule, ns string) bool {
	for _, rule := range ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil {
				if v, ok := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; ok && v == ns {
					return true
				}
			}
		}
	}
	return false
}

// hasPodSelectorRule checks whether the policy's ingress rules contain a podSelector
// matching the given label key/value.
func hasPodSelectorRule(ingress []networkingv1.NetworkPolicyIngressRule, key, value string) bool {
	for _, rule := range ingress {
		for _, peer := range rule.From {
			if peer.PodSelector != nil {
				if v, ok := peer.PodSelector.MatchLabels[key]; ok && v == value {
					return true
				}
			}
		}
	}
	return false
}

// countNamespaceRules counts how many ingress rules have a namespaceSelector matching the given ns.
func countNamespaceRules(ingress []networkingv1.NetworkPolicyIngressRule, ns string) int {
	count := 0
	for _, rule := range ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil {
				if v, ok := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; ok && v == ns {
					count++
				}
			}
		}
	}
	return count
}

func TestBuildNetworkPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model *llmv1alpha1.LLMModel
		cfg   *config.OperatorConfig
		check func(t *testing.T, np *networkingv1.NetworkPolicy)
	}{
		{
			name:  "policy name is <model-name>-deny-direct",
			model: defaultNetworkPolicyModel(),
			cfg:   defaultNetworkPolicyConfig(),
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				if np.Name != "my-model-deny-direct" {
					t.Errorf("expected name %q, got %q", "my-model-deny-direct", np.Name)
				}
			},
		},
		{
			name:  "policy namespace matches model namespace",
			model: defaultNetworkPolicyModel(),
			cfg:   defaultNetworkPolicyConfig(),
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				if np.Namespace != "model-ns" {
					t.Errorf("expected namespace %q, got %q", "model-ns", np.Namespace)
				}
			},
		},
		{
			name:  "podSelector matches model pods via StandardLabels subset",
			model: defaultNetworkPolicyModel(),
			cfg:   defaultNetworkPolicyConfig(),
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				sel := np.Spec.PodSelector.MatchLabels
				if sel["app.kubernetes.io/instance"] != testAuthModelName {
					t.Errorf("expected podSelector app.kubernetes.io/instance=my-model, got %v", sel)
				}
				if sel["llm.nebari.dev/model"] != testAuthModelName {
					t.Errorf("expected podSelector llm.nebari.dev/model=my-model, got %v", sel)
				}
			},
		},
		{
			name:  "policyTypes includes Ingress",
			model: defaultNetworkPolicyModel(),
			cfg:   defaultNetworkPolicyConfig(),
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				found := false
				for _, pt := range np.Spec.PolicyTypes {
					if pt == networkingv1.PolicyTypeIngress {
						found = true
					}
				}
				if !found {
					t.Errorf("expected PolicyTypes to include Ingress, got %v", np.Spec.PolicyTypes)
				}
			},
		},
		{
			name:  "allows ingress from external gateway namespace",
			model: defaultNetworkPolicyModel(),
			cfg: &config.OperatorConfig{
				ExternalGatewayNS: "external-gw-ns",
				InternalGatewayNS: "internal-gw-ns",
			},
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				if !hasNamespaceRule(np.Spec.Ingress, "external-gw-ns") {
					t.Errorf("expected ingress rule allowing from external-gw-ns namespace")
				}
			},
		},
		{
			name:  "allows ingress from internal gateway namespace",
			model: defaultNetworkPolicyModel(),
			cfg: &config.OperatorConfig{
				ExternalGatewayNS: "external-gw-ns",
				InternalGatewayNS: "internal-gw-ns",
			},
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				if !hasNamespaceRule(np.Spec.Ingress, "internal-gw-ns") {
					t.Errorf("expected ingress rule allowing from internal-gw-ns namespace")
				}
			},
		},
		{
			name:  "when both gateway namespaces are the same only one namespace rule exists",
			model: defaultNetworkPolicyModel(),
			cfg: &config.OperatorConfig{
				ExternalGatewayNS: "envoy-gateway-system",
				InternalGatewayNS: "envoy-gateway-system",
			},
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				count := countNamespaceRules(np.Spec.Ingress, "envoy-gateway-system")
				if count != 1 {
					t.Errorf("expected exactly 1 namespace rule for envoy-gateway-system when both gateways share a namespace, got %d", count)
				}
			},
		},
		{
			name:  "allows ingress from EPP pods via podSelector",
			model: defaultNetworkPolicyModel(),
			cfg:   defaultNetworkPolicyConfig(),
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				if !hasPodSelectorRule(np.Spec.Ingress, "app.kubernetes.io/name", "epp") {
					t.Errorf("expected ingress rule allowing from pods with app.kubernetes.io/name=epp")
				}
			},
		},
		{
			name:  "allows ingress from monitoring namespace on port 8000 only",
			model: defaultNetworkPolicyModel(),
			cfg:   defaultNetworkPolicyConfig(),
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				found := false
				for _, rule := range np.Spec.Ingress {
					for _, peer := range rule.From {
						if peer.NamespaceSelector != nil {
							if v, ok := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; ok && v == "monitoring" {
								// Verify port restriction
								if len(rule.Ports) != 1 {
									t.Errorf("expected exactly 1 port in monitoring rule, got %d", len(rule.Ports))
								} else {
									port := rule.Ports[0]
									if port.Port == nil || port.Port.IntValue() != 8000 {
										t.Errorf("expected port 8000 in monitoring rule, got %v", port.Port)
									}
									if port.Protocol == nil || *port.Protocol != "TCP" {
										t.Errorf("expected protocol TCP in monitoring rule, got %v", port.Protocol)
									}
								}
								found = true
							}
						}
					}
				}
				if !found {
					t.Errorf("expected ingress rule allowing from monitoring namespace")
				}
			},
		},
		{
			name:  "labels set to StandardLabels",
			model: defaultNetworkPolicyModel(),
			cfg:   defaultNetworkPolicyConfig(),
			check: func(t *testing.T, np *networkingv1.NetworkPolicy) {
				expected := StandardLabels(defaultNetworkPolicyModel())
				for k, v := range expected {
					if np.Labels[k] != v {
						t.Errorf("expected label %q=%q, got %q", k, v, np.Labels[k])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			np := BuildNetworkPolicy(tt.model, tt.cfg)
			if np == nil {
				t.Fatal("expected non-nil NetworkPolicy")
			}
			tt.check(t, np)
		})
	}
}
