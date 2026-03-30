package reconcilers

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

// AuthResources holds all auth-related resources for an LLMModel.
type AuthResources struct {
	// Resources in the llm-api-keys namespace (no ownerReference, cleaned up via finalizer)
	APIKeySecret     *corev1.Secret
	APIKeyMetadataCM *corev1.ConfigMap
	ReferenceGrant   *unstructured.Unstructured // gateway.networking.k8s.io/v1beta1 ReferenceGrant

	// Resources in the model namespace
	ExternalSecurityPolicy *unstructured.Unstructured // apiKeyAuth SecurityPolicy, nil if external disabled
	InternalSecurityPolicy *unstructured.Unstructured // JWT SecurityPolicy, nil if internal disabled
}

// BuildAuthResources is a pure function that computes all auth-related Kubernetes resources
// for the given LLMModel: API key Secret, metadata ConfigMap, ReferenceGrant, and SecurityPolicies.
func BuildAuthResources(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) (*AuthResources, error) {
	result := &AuthResources{}

	// Shared labels for cross-namespace resources (Secret and ConfigMap in api-keys namespace)
	crossNSLabels := buildCrossNSLabels(model)

	result.APIKeySecret = buildAPIKeySecret(model, cfg, crossNSLabels)
	result.APIKeyMetadataCM = buildAPIKeyMetadataConfigMap(model, cfg, crossNSLabels)
	result.ReferenceGrant = buildReferenceGrant(model, cfg)

	if boolOrDefault(model.Spec.Endpoints.External.Enabled, true) {
		result.ExternalSecurityPolicy = buildExternalSecurityPolicy(model, cfg)
	}

	if boolOrDefault(model.Spec.Endpoints.Internal.Enabled, true) {
		result.InternalSecurityPolicy = buildInternalSecurityPolicy(model, cfg)
	}

	return result, nil
}

// buildCrossNSLabels returns labels for resources that live in the api-keys namespace
// but belong to a specific model in another namespace.
func buildCrossNSLabels(model *llmv1alpha1.LLMModel) map[string]string {
	labels := StandardLabels(model)
	labels["llm.nebari.dev/model-name"] = model.Name
	labels["llm.nebari.dev/model-namespace"] = model.Namespace
	return labels
}

func buildAPIKeySecret(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig, labels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      APIKeySecretName(model.Name),
			Namespace: cfg.APIKeysNamespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func buildAPIKeyMetadataConfigMap(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig, labels map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      APIKeyMetadataConfigMapName(model.Name),
			Namespace: cfg.APIKeysNamespace,
			Labels:    labels,
		},
	}
}

func buildReferenceGrant(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) *unstructured.Unstructured {
	name := model.Name + "-" + model.Namespace + "-ref-grant"
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1beta1",
			"kind":       "ReferenceGrant",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": cfg.APIKeysNamespace,
			},
			"spec": map[string]interface{}{
				"from": []interface{}{
					map[string]interface{}{
						"group":     "gateway.envoyproxy.io",
						"kind":      "SecurityPolicy",
						"namespace": model.Namespace,
					},
				},
				"to": []interface{}{
					map[string]interface{}{
						"group": "",
						"kind":  "Secret",
						"name":  APIKeySecretName(model.Name),
					},
				},
			},
		},
	}
}

func buildExternalSecurityPolicy(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) *unstructured.Unstructured {
	name := model.Name + "-external-auth"
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.envoyproxy.io/v1alpha1",
			"kind":       "SecurityPolicy",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": model.Namespace,
				"labels":    labelsToInterface(StandardLabels(model)),
			},
			"spec": map[string]interface{}{
				"targetRefs": []interface{}{
					map[string]interface{}{
						"group": "gateway.networking.k8s.io",
						"kind":  "HTTPRoute",
						"name":  model.Name + "-external",
					},
				},
				"apiKeyAuth": map[string]interface{}{
					"credentialRefs": []interface{}{
						map[string]interface{}{
							"group":     "",
							"kind":      "Secret",
							"name":      APIKeySecretName(model.Name),
							"namespace": cfg.APIKeysNamespace,
						},
					},
					"extractFrom": []interface{}{
						map[string]interface{}{
							"headers": []interface{}{
								"Authorization",
							},
						},
					},
					"sanitize":              true,
					"forwardClientIDHeader": "X-Client-ID",
				},
			},
		},
	}
}

func buildInternalSecurityPolicy(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) *unstructured.Unstructured {
	name := model.Name + "-internal-auth"

	provider := map[string]interface{}{
		"name":   "oidc",
		"issuer": cfg.OIDCIssuerURL,
		"remoteJWKS": map[string]interface{}{
			"uri": cfg.OIDCIssuerURL + "/.well-known/jwks.json",
		},
		"claimToHeaders": []interface{}{
			map[string]interface{}{
				"header": "X-Auth-Groups",
				"claim":  cfg.OIDCGroupsClaim,
			},
			map[string]interface{}{
				"header": "X-Auth-User",
				"claim":  "preferred_username",
			},
		},
	}

	if cfg.OIDCAudience != "" {
		provider["audiences"] = []interface{}{cfg.OIDCAudience}
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.envoyproxy.io/v1alpha1",
			"kind":       "SecurityPolicy",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": model.Namespace,
				"labels":    labelsToInterface(StandardLabels(model)),
			},
			"spec": map[string]interface{}{
				"targetRefs": []interface{}{
					map[string]interface{}{
						"group": "gateway.networking.k8s.io",
						"kind":  "HTTPRoute",
						"name":  model.Name + "-internal",
					},
				},
				"jwt": map[string]interface{}{
					"providers": []interface{}{
						provider,
					},
				},
			},
		},
	}
}
