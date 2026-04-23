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
func boolOrDefault(b *bool, def bool) bool { //nolint:unparam // def is always true today but the function is used across multiple call sites
	if b == nil {
		return def
	}
	return *b
}

// BuildRoutingResources is a pure function that computes the AIGatewayRoute resources
// for external and internal endpoints of the given model.
//
// Each AIGatewayRoute is rendered by the Envoy AI Gateway controller into an
// HTTPRoute. AIGatewayRouteSpec itself does not currently expose a hostnames
// field (the upstream controller does not propagate hostnames onto the
// generated HTTPRoute), so we ensure deterministic per-model routing by
// constraining every rule with a `Host` header match for the model's FQDN.
// Without a host constraint, every route attached to a Gateway matches every
// request and SecurityPolicy attachment becomes non-deterministic - an
// external (API key) request can be matched against the internal route's JWT
// filter and rejected. See issue #64.
func BuildRoutingResources(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) (*RoutingResources, error) {
	result := &RoutingResources{}

	subdomain, err := EffectiveSubdomain(model)
	if err != nil {
		return nil, err
	}

	if boolOrDefault(model.Spec.Endpoints.External.Enabled, true) {
		result.ExternalRoute = buildAIGatewayRoute(
			model.Name+"-external",
			model.Namespace,
			StandardLabels(model),
			cfg.ExternalGatewayName,
			cfg.ExternalGatewayNS,
			model.Name,
			ExternalHostname(subdomain, cfg.BaseDomain),
		)
	}

	if boolOrDefault(model.Spec.Endpoints.Internal.Enabled, true) {
		result.InternalRoute = buildAIGatewayRoute(
			model.Name+"-internal",
			model.Namespace,
			StandardLabels(model),
			cfg.InternalGatewayName,
			cfg.InternalGatewayNS,
			model.Name,
			InternalHostname(subdomain, cfg.BaseDomain),
		)
	}

	return result, nil
}

func buildAIGatewayRoute(
	name, namespace string,
	labels map[string]string,
	gatewayName, gatewayNS string,
	poolName string,
	hostname string,
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
				"rules": []interface{}{
					map[string]interface{}{
						// Match on the Host header so this rule only fires for
						// the model's FQDN. The Envoy AI Gateway controller
						// propagates Headers matchers onto the generated
						// HTTPRoute, giving us deterministic per-model routing
						// even when multiple routes share a Gateway listener.
						"matches": []interface{}{
							map[string]interface{}{
								"headers": []interface{}{
									map[string]interface{}{
										"type":  "Exact",
										"name":  "Host",
										"value": hostname,
									},
								},
							},
						},
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
