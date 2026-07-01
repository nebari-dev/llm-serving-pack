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
			ExternalHTTPSListenerName,
			model.Name,
			model.Spec.Model.Name,
			SharedExternalHostname(cfg.BaseDomain),
			cfg.RouteRequestTimeout,
		)
	}

	if boolOrDefault(model.Spec.Endpoints.Internal.Enabled, true) {
		result.InternalRoute = buildAIGatewayRoute(
			model.Name+"-internal",
			model.Namespace,
			StandardLabels(model),
			cfg.InternalGatewayName,
			cfg.InternalGatewayNS,
			InternalHTTPSListenerName,
			model.Name,
			model.Spec.Model.Name,
			SharedInternalHostname(cfg.BaseDomain),
			cfg.RouteRequestTimeout,
		)
	}

	return result, nil
}

func buildAIGatewayRoute(
	name, namespace string,
	labels map[string]string,
	gatewayName, gatewayNS string,
	listenerSectionName string,
	poolName string,
	modelName string,
	hostname string,
	requestTimeout string,
) *unstructured.Unstructured {
	rule := map[string]interface{}{
		// Match on BOTH the shared hostname (Host header) AND the
		// `x-ai-eg-model` header. With sectionName scoping on the route's
		// parentRefs (set below), the Host match is defence in depth: it
		// guarantees the rule only fires for the intended FQDN even if a
		// future refactor loosens parent attachment.
		//
		// The x-ai-eg-model match gives us per-model dispatch once a request
		// lands on a shared listener. The Envoy AI Gateway extproc filter
		// derives the value from the request body's `model` field before
		// HTTPRoute matching runs. Both matches are ANDed (per Gateway API
		// spec).
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
	}

	// requestTimeout, when set, becomes spec.rules[].timeouts.request on the
	// AIGatewayRoute. The Envoy AI Gateway copies it onto the HTTPRoute it
	// renders; left unset, the gateway injects its own 60s default, which is
	// too short for many LLM generations (long completions, reasoning models,
	// large prompts, cold model loads). The route timeout is the only
	// effective lever here: an explicit HTTPRoute timeout takes precedence
	// over a BackendTrafficPolicy, so a policy attachment cannot raise it.
	if requestTimeout != "" {
		rule["timeouts"] = map[string]interface{}{
			"request": requestTimeout,
		}
	}

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
				// sectionName scopes this HTTPRoute attachment to a single
				// named listener on the parent Gateway. Required because the
				// AI Gateway controller auto-appends a catch-all "route-not-
				// found" rule (path: / with no host/header constraints) to
				// every generated HTTPRoute; without sectionName, that rule
				// attaches to every listener on the Gateway and any
				// SecurityPolicy bound to this HTTPRoute catches traffic
				// bound for unrelated hostnames (key-manager UI, argocd,
				// keycloak, base domain). Regression seen in alpha.3/alpha.4;
				// the Host header matcher on the first rule narrowed that
				// rule but had no effect on the catch-all.
				"parentRefs": []interface{}{
					map[string]interface{}{
						"name":        gatewayName,
						"namespace":   gatewayNS,
						"sectionName": listenerSectionName,
					},
				},
				"rules": []interface{}{rule},
			},
		},
	}
}
