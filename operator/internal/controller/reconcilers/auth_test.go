package reconcilers

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

func defaultAuthModel(name string) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "test-ns",
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   "mistralai/Mistral-7B-v0.1",
				Source: llmv1alpha1.ModelSourceHuggingFace,
			},
		},
	}
}

func defaultAuthConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:          "example.com",
		ExternalGatewayName: "external-gw",
		ExternalGatewayNS:   "envoy-gateway-system",
		InternalGatewayName: "internal-gw",
		InternalGatewayNS:   "envoy-gateway-system",
		OIDCIssuerURL:       "https://oidc.example.com",
		OIDCGroupsClaim:     "groups",
		OIDCAudience:        "my-audience",
		APIKeysNamespace:    "llm-api-keys",
	}
}

func TestBuildAuthResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model *llmv1alpha1.LLMModel
		cfg   *config.OperatorConfig
		check func(t *testing.T, result *AuthResources, err error)
	}{
		{
			name:  "API key Secret: correct name and namespace",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.APIKeySecret == nil {
					t.Fatal("expected APIKeySecret to be non-nil")
				}
				if result.APIKeySecret.Name != APIKeySecretName("my-model") {
					t.Errorf("expected name %q, got %q", APIKeySecretName("my-model"), result.APIKeySecret.Name)
				}
				if result.APIKeySecret.Namespace != "llm-api-keys" {
					t.Errorf("expected namespace llm-api-keys, got %q", result.APIKeySecret.Namespace)
				}
			},
		},
		{
			name:  "API key Secret: empty data and Opaque type",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result.APIKeySecret.Data) != 0 {
					t.Errorf("expected empty Secret data, got %d keys", len(result.APIKeySecret.Data))
				}
				if string(result.APIKeySecret.Type) != "" && result.APIKeySecret.Type != "Opaque" {
					t.Errorf("expected Opaque type, got %q", result.APIKeySecret.Type)
				}
			},
		},
		{
			name:  "API key ConfigMap: correct name and namespace",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.APIKeyMetadataCM == nil {
					t.Fatal("expected APIKeyMetadataCM to be non-nil")
				}
				if result.APIKeyMetadataCM.Name != APIKeyMetadataConfigMapName("my-model") {
					t.Errorf("expected name %q, got %q", APIKeyMetadataConfigMapName("my-model"), result.APIKeyMetadataCM.Name)
				}
				if result.APIKeyMetadataCM.Namespace != "llm-api-keys" {
					t.Errorf("expected namespace llm-api-keys, got %q", result.APIKeyMetadataCM.Namespace)
				}
			},
		},
		{
			name:  "API key ConfigMap: empty data",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result.APIKeyMetadataCM.Data) != 0 {
					t.Errorf("expected empty ConfigMap data, got %d keys", len(result.APIKeyMetadataCM.Data))
				}
			},
		},
		{
			name:  "Labels: Secret and ConfigMap include model-name and model-namespace labels",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				for _, resource := range []struct {
					name   string
					labels map[string]string
				}{
					{"Secret", result.APIKeySecret.Labels},
					{"ConfigMap", result.APIKeyMetadataCM.Labels},
				} {
					if resource.labels["llm.nebari.dev/model-name"] != "my-model" {
						t.Errorf("%s: expected model-name label my-model, got %q", resource.name, resource.labels["llm.nebari.dev/model-name"])
					}
					if resource.labels["llm.nebari.dev/model-namespace"] != "test-ns" {
						t.Errorf("%s: expected model-namespace label test-ns, got %q", resource.name, resource.labels["llm.nebari.dev/model-namespace"])
					}
					if resource.labels["app.kubernetes.io/managed-by"] != "nebari-llm-operator" {
						t.Errorf("%s: expected managed-by label, got %q", resource.name, resource.labels["app.kubernetes.io/managed-by"])
					}
				}
			},
		},
		{
			name:  "ReferenceGrant: correct apiVersion and kind",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ReferenceGrant == nil {
					t.Fatal("expected ReferenceGrant to be non-nil")
				}
				if result.ReferenceGrant.GetAPIVersion() != "gateway.networking.k8s.io/v1beta1" {
					t.Errorf("expected apiVersion gateway.networking.k8s.io/v1beta1, got %q", result.ReferenceGrant.GetAPIVersion())
				}
				if result.ReferenceGrant.GetKind() != "ReferenceGrant" {
					t.Errorf("expected kind ReferenceGrant, got %q", result.ReferenceGrant.GetKind())
				}
			},
		},
		{
			name:  "ReferenceGrant: namespace is cfg.APIKeysNamespace",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ReferenceGrant.GetNamespace() != "llm-api-keys" {
					t.Errorf("expected namespace llm-api-keys, got %q", result.ReferenceGrant.GetNamespace())
				}
			},
		},
		{
			name:  "ReferenceGrant: name includes both model name and namespace for uniqueness",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				expectedName := "my-model-test-ns-ref-grant"
				if result.ReferenceGrant.GetName() != expectedName {
					t.Errorf("expected name %q, got %q", expectedName, result.ReferenceGrant.GetName())
				}
			},
		},
		{
			name:  "ReferenceGrant: correct from (SecurityPolicy in model namespace)",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ReferenceGrant.Object["spec"].(map[string]interface{})
				from := spec["from"].([]interface{})
				if len(from) != 1 {
					t.Fatalf("expected 1 from entry, got %d", len(from))
				}
				fromEntry := from[0].(map[string]interface{})
				if fromEntry["group"] != "gateway.envoyproxy.io" {
					t.Errorf("expected from group gateway.envoyproxy.io, got %q", fromEntry["group"])
				}
				if fromEntry["kind"] != "SecurityPolicy" {
					t.Errorf("expected from kind SecurityPolicy, got %q", fromEntry["kind"])
				}
				if fromEntry["namespace"] != "test-ns" {
					t.Errorf("expected from namespace test-ns, got %q", fromEntry["namespace"])
				}
			},
		},
		{
			name:  "ReferenceGrant: correct to (Secret by name in api-keys namespace)",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ReferenceGrant.Object["spec"].(map[string]interface{})
				to := spec["to"].([]interface{})
				if len(to) != 1 {
					t.Fatalf("expected 1 to entry, got %d", len(to))
				}
				toEntry := to[0].(map[string]interface{})
				if toEntry["group"] != "" {
					t.Errorf("expected to group empty string, got %q", toEntry["group"])
				}
				if toEntry["kind"] != "Secret" {
					t.Errorf("expected to kind Secret, got %q", toEntry["kind"])
				}
				if toEntry["name"] != APIKeySecretName("my-model") {
					t.Errorf("expected to name %q, got %q", APIKeySecretName("my-model"), toEntry["name"])
				}
			},
		},
		{
			name:  "External SecurityPolicy: correct targetRef to HTTPRoute <name>-external",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalSecurityPolicy == nil {
					t.Fatal("expected ExternalSecurityPolicy to be non-nil")
				}
				spec := result.ExternalSecurityPolicy.Object["spec"].(map[string]interface{})
				targetRefs := spec["targetRefs"].([]interface{})
				if len(targetRefs) != 1 {
					t.Fatalf("expected 1 targetRef, got %d", len(targetRefs))
				}
				targetRef := targetRefs[0].(map[string]interface{})
				if targetRef["group"] != "gateway.networking.k8s.io" {
					t.Errorf("expected group gateway.networking.k8s.io, got %q", targetRef["group"])
				}
				if targetRef["kind"] != "HTTPRoute" {
					t.Errorf("expected kind HTTPRoute, got %q", targetRef["kind"])
				}
				if targetRef["name"] != "my-model-external" {
					t.Errorf("expected name my-model-external, got %q", targetRef["name"])
				}
			},
		},
		{
			name:  "External SecurityPolicy: apiKeyAuth credentialRef to Secret in api-keys namespace",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ExternalSecurityPolicy.Object["spec"].(map[string]interface{})
				apiKeyAuth := spec["apiKeyAuth"].(map[string]interface{})
				credentialRefs := apiKeyAuth["credentialRefs"].([]interface{})
				if len(credentialRefs) != 1 {
					t.Fatalf("expected 1 credentialRef, got %d", len(credentialRefs))
				}
				credRef := credentialRefs[0].(map[string]interface{})
				if credRef["group"] != "" {
					t.Errorf("expected group empty string, got %q", credRef["group"])
				}
				if credRef["kind"] != "Secret" {
					t.Errorf("expected kind Secret, got %q", credRef["kind"])
				}
				if credRef["name"] != APIKeySecretName("my-model") {
					t.Errorf("expected name %q, got %q", APIKeySecretName("my-model"), credRef["name"])
				}
				if credRef["namespace"] != "llm-api-keys" {
					t.Errorf("expected namespace llm-api-keys, got %q", credRef["namespace"])
				}
			},
		},
		{
			name:  "External SecurityPolicy: sanitize true and forwardClientIDHeader X-Client-ID",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ExternalSecurityPolicy.Object["spec"].(map[string]interface{})
				apiKeyAuth := spec["apiKeyAuth"].(map[string]interface{})
				if apiKeyAuth["sanitize"] != true {
					t.Errorf("expected sanitize true, got %v", apiKeyAuth["sanitize"])
				}
				if apiKeyAuth["forwardClientIDHeader"] != "X-Client-ID" {
					t.Errorf("expected forwardClientIDHeader X-Client-ID, got %q", apiKeyAuth["forwardClientIDHeader"])
				}
			},
		},
		{
			name:  "External SecurityPolicy: extractFrom headers includes Authorization",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ExternalSecurityPolicy.Object["spec"].(map[string]interface{})
				apiKeyAuth := spec["apiKeyAuth"].(map[string]interface{})
				extractFromArr := apiKeyAuth["extractFrom"].([]interface{})
				if len(extractFromArr) == 0 {
					t.Fatal("expected at least one extractFrom entry")
				}
				extractFrom := extractFromArr[0].(map[string]interface{})
				headers := extractFrom["headers"].([]interface{})
				if len(headers) == 0 {
					t.Fatal("expected at least one extractFrom header")
				}
				found := false
				for _, h := range headers {
					if h == "Authorization" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected Authorization header in extractFrom.headers, got %v", headers)
				}
			},
		},
		{
			name: "External disabled: ExternalSecurityPolicy is nil",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultAuthModel("my-model")
				m.Spec.Endpoints.External.Enabled = boolPtr(false)
				return m
			}(),
			cfg: defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalSecurityPolicy != nil {
					t.Error("expected ExternalSecurityPolicy to be nil when external disabled")
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: correct targetRef to HTTPRoute <name>-internal",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InternalSecurityPolicy == nil {
					t.Fatal("expected InternalSecurityPolicy to be non-nil")
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				targetRefs := spec["targetRefs"].([]interface{})
				if len(targetRefs) != 1 {
					t.Fatalf("expected 1 targetRef, got %d", len(targetRefs))
				}
				targetRef := targetRefs[0].(map[string]interface{})
				if targetRef["group"] != "gateway.networking.k8s.io" {
					t.Errorf("expected group gateway.networking.k8s.io, got %q", targetRef["group"])
				}
				if targetRef["kind"] != "HTTPRoute" {
					t.Errorf("expected kind HTTPRoute, got %q", targetRef["kind"])
				}
				if targetRef["name"] != "my-model-internal" {
					t.Errorf("expected name my-model-internal, got %q", targetRef["name"])
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: JWT provider with correct issuer and JWKS URI",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				jwt := spec["jwt"].(map[string]interface{})
				providers := jwt["providers"].([]interface{})
				if len(providers) != 1 {
					t.Fatalf("expected 1 JWT provider, got %d", len(providers))
				}
				provider := providers[0].(map[string]interface{})
				if provider["name"] != "oidc" {
					t.Errorf("expected provider name oidc, got %q", provider["name"])
				}
				if provider["issuer"] != "https://oidc.example.com" {
					t.Errorf("expected issuer https://oidc.example.com, got %q", provider["issuer"])
				}
				remoteJWKS := provider["remoteJWKS"].(map[string]interface{})
				expectedURI := "https://oidc.example.com/.well-known/jwks.json"
				if remoteJWKS["uri"] != expectedURI {
					t.Errorf("expected JWKS URI %q, got %q", expectedURI, remoteJWKS["uri"])
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: audiences set when OIDCAudience is non-empty",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				jwt := spec["jwt"].(map[string]interface{})
				providers := jwt["providers"].([]interface{})
				provider := providers[0].(map[string]interface{})
				audiences, ok := provider["audiences"]
				if !ok {
					t.Fatal("expected audiences to be present when OIDCAudience is non-empty")
				}
				audienceList := audiences.([]interface{})
				if len(audienceList) != 1 || audienceList[0] != "my-audience" {
					t.Errorf("expected audiences [my-audience], got %v", audienceList)
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: audiences omitted when OIDCAudience is empty",
			model: defaultAuthModel("my-model"),
			cfg: func() *config.OperatorConfig {
				cfg := defaultAuthConfig()
				cfg.OIDCAudience = ""
				return cfg
			}(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				jwt := spec["jwt"].(map[string]interface{})
				providers := jwt["providers"].([]interface{})
				provider := providers[0].(map[string]interface{})
				if _, ok := provider["audiences"]; ok {
					t.Error("expected audiences to be omitted when OIDCAudience is empty")
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: claimToHeaders maps groups and username",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				jwt := spec["jwt"].(map[string]interface{})
				providers := jwt["providers"].([]interface{})
				provider := providers[0].(map[string]interface{})
				claimToHeaders := provider["claimToHeaders"].([]interface{})
				if len(claimToHeaders) < 2 {
					t.Fatalf("expected at least 2 claimToHeaders entries, got %d", len(claimToHeaders))
				}
				// Check groups mapping
				foundGroups := false
				foundUsername := false
				for _, cth := range claimToHeaders {
					entry := cth.(map[string]interface{})
					if entry["header"] == "X-Auth-Groups" && entry["claim"] == "groups" {
						foundGroups = true
					}
					if entry["header"] == "X-Auth-User" && entry["claim"] == "preferred_username" {
						foundUsername = true
					}
				}
				if !foundGroups {
					t.Error("expected claimToHeaders entry for X-Auth-Groups -> groups")
				}
				if !foundUsername {
					t.Error("expected claimToHeaders entry for X-Auth-User -> preferred_username")
				}
			},
		},
		{
			name: "Internal disabled: InternalSecurityPolicy is nil",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultAuthModel("my-model")
				m.Spec.Endpoints.Internal.Enabled = boolPtr(false)
				return m
			}(),
			cfg: defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InternalSecurityPolicy != nil {
					t.Error("expected InternalSecurityPolicy to be nil when internal disabled")
				}
			},
		},
		{
			name: "Both disabled: both SecurityPolicies nil, but Secret/ConfigMap/ReferenceGrant still created",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultAuthModel("my-model")
				m.Spec.Endpoints.External.Enabled = boolPtr(false)
				m.Spec.Endpoints.Internal.Enabled = boolPtr(false)
				return m
			}(),
			cfg: defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalSecurityPolicy != nil {
					t.Error("expected ExternalSecurityPolicy to be nil when both disabled")
				}
				if result.InternalSecurityPolicy != nil {
					t.Error("expected InternalSecurityPolicy to be nil when both disabled")
				}
				if result.APIKeySecret == nil {
					t.Error("expected APIKeySecret to be non-nil even when both endpoints disabled")
				}
				if result.APIKeyMetadataCM == nil {
					t.Error("expected APIKeyMetadataCM to be non-nil even when both endpoints disabled")
				}
				if result.ReferenceGrant == nil {
					t.Error("expected ReferenceGrant to be non-nil even when both endpoints disabled")
				}
			},
		},
		{
			name:  "External SecurityPolicy: correct name and namespace",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalSecurityPolicy.GetName() != "my-model-external-auth" {
					t.Errorf("expected name my-model-external-auth, got %q", result.ExternalSecurityPolicy.GetName())
				}
				if result.ExternalSecurityPolicy.GetNamespace() != "test-ns" {
					t.Errorf("expected namespace test-ns, got %q", result.ExternalSecurityPolicy.GetNamespace())
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: correct name and namespace",
			model: defaultAuthModel("my-model"),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InternalSecurityPolicy.GetName() != "my-model-internal-auth" {
					t.Errorf("expected name my-model-internal-auth, got %q", result.InternalSecurityPolicy.GetName())
				}
				if result.InternalSecurityPolicy.GetNamespace() != "test-ns" {
					t.Errorf("expected namespace test-ns, got %q", result.InternalSecurityPolicy.GetNamespace())
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := BuildAuthResources(tt.model, tt.cfg)
			tt.check(t, result, err)
		})
	}
}
