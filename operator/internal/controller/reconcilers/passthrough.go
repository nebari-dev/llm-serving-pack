package reconcilers

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

// PassthroughResources holds every resource generated for a PassthroughModel.
// Provider plumbing (Backend, BackendTLSPolicy, AIServiceBackend,
// BackendSecurityPolicy) connects the gateway to the external provider; the
// routes and SecurityPolicies mirror the per-model external/internal pair the
// LLMModel reconciler builds, and the api-keys Secret + metadata ConfigMap
// give the key-manager the exact same surface it has for served models.
type PassthroughResources struct {
	Backend               *unstructured.Unstructured
	BackendTLSPolicy      *unstructured.Unstructured
	AIServiceBackend      *unstructured.Unstructured
	BackendSecurityPolicy *unstructured.Unstructured

	APIKeySecret     *corev1.Secret
	APIKeyMetadataCM *corev1.ConfigMap

	ExternalRoute          *unstructured.Unstructured // nil if external disabled
	InternalRoute          *unstructured.Unstructured // nil if internal disabled
	ExternalSecurityPolicy *unstructured.Unstructured // nil if external disabled
	InternalSecurityPolicy *unstructured.Unstructured // nil if internal disabled
}

// PassthroughStandardLabels returns the standard labels for resources managed
// for a PassthroughModel. The `llm.nebari.dev/model` key matches the LLMModel
// convention so existing label selectors (docs, key-manager) keep working.
func PassthroughStandardLabels(pm *llmv1alpha1.PassthroughModel) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "nebari-llm-operator",
		"app.kubernetes.io/instance":   pm.Name,
		"app.kubernetes.io/name":       "passthroughmodel",
		"llm.nebari.dev/model":         pm.Name,
	}
}

// BuildPassthroughResources is a pure function that computes every resource
// for the given PassthroughModel. No serving, storage, or scheduling
// resources are involved; the provider serves the models, we route to it.
func BuildPassthroughResources(pm *llmv1alpha1.PassthroughModel, cfg *config.OperatorConfig) (*PassthroughResources, error) {
	labels := PassthroughStandardLabels(pm)
	authLabels := map[string]string{}
	for k, v := range labels {
		authLabels[k] = v
	}
	// The key-manager filters API-key metadata ConfigMaps on this label,
	// same as the LLMModel path (see authResourceLabels).
	authLabels["llm.nebari.dev/model-name"] = pm.Name

	result := &PassthroughResources{
		Backend:               buildProviderBackend(pm, labels),
		BackendTLSPolicy:      buildProviderBackendTLSPolicy(pm, labels),
		AIServiceBackend:      buildProviderAIServiceBackend(pm, labels),
		BackendSecurityPolicy: buildProviderBackendSecurityPolicy(pm, labels),
		APIKeySecret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      APIKeySecretName(pm.Name),
				Namespace: pm.Namespace,
				Labels:    authLabels,
			},
			Type: corev1.SecretTypeOpaque,
		},
		APIKeyMetadataCM: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      APIKeyMetadataConfigMapName(pm.Name),
				Namespace: pm.Namespace,
				Labels:    authLabels,
			},
		},
	}

	if boolOrDefault(pm.Spec.Endpoints.External.Enabled, true) {
		result.ExternalRoute = buildPassthroughRoute(
			pm.Name+"-external",
			pm,
			labels,
			cfg.ExternalGatewayName,
			cfg.ExternalGatewayNS,
			ExternalHTTPSListenerName,
			SharedExternalHostname(cfg.BaseDomain),
			// modelsOwnedBy only on the external route: the gateway's
			// /v1/models endpoint aggregates declared models across every
			// route on the Gateway, so declaring them on both endpoints
			// would list each model twice.
			true,
		)
		result.ExternalSecurityPolicy = buildAPIKeyAuthSecurityPolicy(
			pm.Name+"-external-auth",
			pm.Namespace,
			labelsToInterface(labels),
			pm.Name+"-external",
			APIKeySecretName(pm.Name),
		)
	}

	if boolOrDefault(pm.Spec.Endpoints.Internal.Enabled, true) {
		result.InternalRoute = buildPassthroughRoute(
			pm.Name+"-internal",
			pm,
			labels,
			cfg.InternalGatewayName,
			cfg.InternalGatewayNS,
			InternalHTTPSListenerName,
			SharedInternalHostname(cfg.BaseDomain),
			false,
		)
		result.InternalSecurityPolicy = buildJWTSecurityPolicy(
			pm.Name+"-internal-auth",
			pm.Namespace,
			labelsToInterface(labels),
			pm.Name+"-internal",
			cfg,
			isPublicAccess(pm.Spec.Access),
			pm.Spec.Access.Groups,
		)
	}

	return result, nil
}

// isPublicAccess returns true when the access policy is explicitly public.
func isPublicAccess(access llmv1alpha1.AccessSpec) bool {
	return access.Public != nil && *access.Public
}

func providerBackendName(pm *llmv1alpha1.PassthroughModel) string {
	return pm.Name + "-backend"
}

func buildProviderBackend(pm *llmv1alpha1.PassthroughModel, labels map[string]string) *unstructured.Unstructured {
	port := pm.Spec.Provider.Port
	if port == 0 {
		port = 443
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.envoyproxy.io/v1alpha1",
			"kind":       "Backend",
			"metadata": map[string]interface{}{
				"name":      providerBackendName(pm),
				"namespace": pm.Namespace,
				"labels":    labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"fqdn": map[string]interface{}{
							"hostname": pm.Spec.Provider.Hostname,
							"port":     int64(port),
						},
					},
				},
			},
		},
	}
}

func buildProviderBackendTLSPolicy(pm *llmv1alpha1.PassthroughModel, labels map[string]string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			// v1alpha3 rather than v1: Envoy Gateway v1.3 (the minimum the
			// pack supports) predates the v1 BackendTLSPolicy API. Bump
			// alongside the minimum Envoy Gateway version.
			"apiVersion": "gateway.networking.k8s.io/v1alpha3",
			"kind":       "BackendTLSPolicy",
			"metadata": map[string]interface{}{
				"name":      pm.Name + "-backend-tls",
				"namespace": pm.Namespace,
				"labels":    labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"targetRefs": []interface{}{
					map[string]interface{}{
						"group": "gateway.envoyproxy.io",
						"kind":  "Backend",
						"name":  providerBackendName(pm),
					},
				},
				"validation": map[string]interface{}{
					"wellKnownCACertificates": "System",
					"hostname":                pm.Spec.Provider.Hostname,
				},
			},
		},
	}
}

func buildProviderAIServiceBackend(pm *llmv1alpha1.PassthroughModel, labels map[string]string) *unstructured.Unstructured {
	schemaVersion := pm.Spec.Provider.SchemaVersion
	if schemaVersion == "" {
		schemaVersion = "v1"
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "aigateway.envoyproxy.io/v1alpha1",
			"kind":       "AIServiceBackend",
			"metadata": map[string]interface{}{
				"name":      pm.Name,
				"namespace": pm.Namespace,
				"labels":    labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				// All supported providers speak the OpenAI wire protocol;
				// schemaVersion carries the provider's path prefix (e.g.
				// "api/v1" for OpenRouter).
				"schema": map[string]interface{}{
					"name":    "OpenAI",
					"version": schemaVersion,
				},
				"backendRef": map[string]interface{}{
					"group": "gateway.envoyproxy.io",
					"kind":  "Backend",
					"name":  providerBackendName(pm),
				},
			},
		},
	}
}

func buildProviderBackendSecurityPolicy(pm *llmv1alpha1.PassthroughModel, labels map[string]string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "aigateway.envoyproxy.io/v1alpha1",
			"kind":       "BackendSecurityPolicy",
			"metadata": map[string]interface{}{
				"name":      pm.Name + "-upstream-auth",
				"namespace": pm.Namespace,
				"labels":    labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"targetRefs": []interface{}{
					map[string]interface{}{
						"group": "aigateway.envoyproxy.io",
						"kind":  "AIServiceBackend",
						"name":  pm.Name,
					},
				},
				"type": "APIKey",
				"apiKey": map[string]interface{}{
					// Platform-owned provider key (Secret key "apiKey"),
					// injected upstream by the gateway. End users
					// authenticate with their own keys or JWTs and never
					// see this credential.
					"secretRef": map[string]interface{}{
						"name": pm.Spec.Provider.CredentialSecretName,
					},
				},
			},
		},
	}
}

// buildPassthroughRoute renders the AIGatewayRoute for one endpoint of a
// PassthroughModel. Rule order matters only for readability: Gateway API
// precedence (more header matches wins) is what keeps served LLMModels,
// whose routes match Host AND x-ai-eg-model, ahead of the catch-all rule.
//
// sectionName scoping is load-bearing for the same reason as in
// buildAIGatewayRoute: the AI Gateway controller appends a catch-all
// "route-not-found" rule to every generated HTTPRoute, and without
// sectionName that rule (and the SecurityPolicies bound to this route)
// would catch traffic for unrelated listeners on the shared Gateway.
func buildPassthroughRoute(
	name string,
	pm *llmv1alpha1.PassthroughModel,
	labels map[string]string,
	gatewayName, gatewayNS string,
	listenerSectionName string,
	hostname string,
	declareModels bool,
) *unstructured.Unstructured {
	hostHeader := map[string]interface{}{
		"type":  "Exact",
		"name":  "Host",
		"value": hostname,
	}

	rules := []interface{}{}

	if len(pm.Spec.Models.Declared) > 0 {
		matches := make([]interface{}, 0, len(pm.Spec.Models.Declared))
		for _, id := range pm.Spec.Models.Declared {
			matches = append(matches, map[string]interface{}{
				"headers": []interface{}{
					hostHeader,
					map[string]interface{}{
						"type":  "Exact",
						"name":  "x-ai-eg-model",
						"value": id,
					},
				},
			})
		}
		declared := map[string]interface{}{
			"matches":     matches,
			"backendRefs": passthroughBackendRefs(pm),
			"timeouts":    map[string]interface{}{"request": "120s"},
		}
		if declareModels {
			declared["modelsOwnedBy"] = pm.Name
		}
		rules = append(rules, declared)
	}

	if pm.Spec.Models.CatchAll {
		rules = append(rules, map[string]interface{}{
			"matches": []interface{}{
				map[string]interface{}{
					"headers": []interface{}{hostHeader},
				},
			},
			"backendRefs": passthroughBackendRefs(pm),
			"timeouts":    map[string]interface{}{"request": "120s"},
		})
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "aigateway.envoyproxy.io/v1alpha1",
			"kind":       "AIGatewayRoute",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": pm.Namespace,
				"labels":    labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"parentRefs": []interface{}{
					map[string]interface{}{
						"name":        gatewayName,
						"namespace":   gatewayNS,
						"sectionName": listenerSectionName,
					},
				},
				"rules": rules,
			},
		},
	}
}

func passthroughBackendRefs(pm *llmv1alpha1.PassthroughModel) []interface{} {
	return []interface{}{
		map[string]interface{}{
			"name": pm.Name,
		},
	}
}
