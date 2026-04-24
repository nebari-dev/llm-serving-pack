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
// Routing model: every model on the cluster shares a single hostname pair
// (`llm.<baseDomain>` external, `llm-internal.<baseDomain>` internal). This lets
// one TLS certificate, issued via HTTP-01, serve every model. We disambiguate
// models with a per-route header matcher on `x-ai-eg-model`. The Envoy AI
// Gateway controller automatically extracts the `model` field from the JSON
// request body of OpenAI-compatible requests and surfaces it as that header
// before HTTPRoute matching runs (verified against envoyproxy/ai-gateway main).
// Each AIGatewayRoute is rendered into an HTTPRoute that matches its own
// `x-ai-eg-model` value, giving us deterministic per-model routing even when
// every route shares a Gateway listener. SecurityPolicy attachment stays
// per-HTTPRoute (per model), so per-model API keys and per-model JWT group
// authorisation are unchanged.
//
// This supersedes the per-model `Host` header design from #64. The Host header
// approach worked but required a TLS SAN per model, which is incompatible with
// HTTP-01 issuance (no wildcards). Revisit if the AI Gateway changes how it
// surfaces the model identifier or if wildcard certs become available.
func BuildRoutingResources(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) (*RoutingResources, error) {
	result := &RoutingResources{}

	if boolOrDefault(model.Spec.Endpoints.External.Enabled, true) {
		result.ExternalRoute = buildAIGatewayRoute(
			model.Name+"-external",
			model.Namespace,
			StandardLabels(model),
			cfg.ExternalGatewayName,
			cfg.ExternalGatewayNS,
			model.Name,
			model.Spec.Model.Name,
			SharedExternalHostname(cfg.BaseDomain),
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
			model.Spec.Model.Name,
			SharedInternalHostname(cfg.BaseDomain),
		)
	}

	return result, nil
}

func buildAIGatewayRoute(
	name, namespace string,
	labels map[string]string,
	gatewayName, gatewayNS string,
	poolName string,
	modelName string,
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
						// Match on BOTH the shared hostname (Host header) AND the
						// `x-ai-eg-model` header.
						//
						// The Host match scopes this rule to the listener for
						// the shared hostname, keeping unrelated listeners on
						// the same Gateway (llm-keys, argocd, keycloak, the
						// base domain, etc.) from routing traffic here. The AI
						// Gateway controller does not propagate a hostnames
						// field onto the generated HTTPRoute, so without the
						// Host match the route attaches to every listener.
						//
						// The x-ai-eg-model match gives us per-model dispatch
						// once a request lands on a shared hostname. The Envoy
						// AI Gateway extproc filter derives the value from the
						// request body's `model` field before HTTPRoute
						// matching runs. Both matches are ANDed (per Gateway
						// API spec).
						//
						// Regression fixed: alpha.3 shipped only the header
						// match, causing requests to llm-keys.<base> to hit
						// per-model APIKeyAuth SecurityPolicy.
						"matches": []interface{}{
							map[string]interface{}{
								"headers": []interface{}{
									map[string]interface{}{
										"type":  "Exact",
										"name":  "Host",
										"value": hostname,
									},
									map[string]interface{}{
										"type":  "Exact",
										"name":  "x-ai-eg-model",
										"value": modelName,
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
