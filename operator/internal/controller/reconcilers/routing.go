package reconcilers

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

// RoutingResources holds the AIGatewayRoute resources for external and internal endpoints.
type RoutingResources struct {
	ExternalRoute *unstructured.Unstructured // AIGatewayRoute for external endpoint, nil if disabled
	InternalRoute *unstructured.Unstructured // AIGatewayRoute for internal endpoint, nil if disabled
}

// boolOrDefault returns the value of b if non-nil, otherwise returns def.
func boolOrDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// BuildRoutingResources is a pure function that computes the AIGatewayRoute resources
// for external and internal endpoints of the given model.
func BuildRoutingResources(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) (*RoutingResources, error) {
	result := &RoutingResources{}

	if boolOrDefault(model.Spec.Endpoints.External.Enabled, true) {
		subdomain := model.Spec.Endpoints.External.Subdomain
		if subdomain == "" {
			subdomain = Slugify(model.Name)
		}
		result.ExternalRoute = buildAIGatewayRoute(
			model.Name+"-external",
			model.Namespace,
			StandardLabels(model),
			cfg.ExternalGatewayName,
			cfg.ExternalGatewayNS,
			ExternalHostname(subdomain, cfg.BaseDomain),
			model.Name,
		)
	}

	if boolOrDefault(model.Spec.Endpoints.Internal.Enabled, true) {
		subdomain := model.Spec.Endpoints.External.Subdomain
		if subdomain == "" {
			subdomain = Slugify(model.Name)
		}
		result.InternalRoute = buildAIGatewayRoute(
			model.Name+"-internal",
			model.Namespace,
			StandardLabels(model),
			cfg.InternalGatewayName,
			cfg.InternalGatewayNS,
			InternalHostname(subdomain, cfg.BaseDomain),
			model.Name,
		)
	}

	return result, nil
}

func buildAIGatewayRoute(
	name, namespace string,
	labels map[string]string,
	gatewayName, gatewayNS string,
	hostname string,
	poolName string,
) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "aigateway.envoyproxy.io/v1alpha1",
			"kind":       "AIGatewayRoute",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels":    labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"parentRefs": []interface{}{
					map[string]interface{}{
						"name":      gatewayName,
						"namespace": gatewayNS,
					},
				},
				"hostnames": []interface{}{
					hostname,
				},
				"rules": []interface{}{
					map[string]interface{}{
						"backendRefs": []interface{}{
							map[string]interface{}{
								"group": "inference.networking.k8s.io",
								"kind":  "InferencePool",
								"name":  poolName,
							},
						},
					},
				},
			},
		},
	}
}
