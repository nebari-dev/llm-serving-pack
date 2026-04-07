package reconcilers

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

const (
	testAuthModelName = "my-model"
	testAuthNamespace = "test-ns"
	testAuthAPIKeysNS = "llm-api-keys"
	testAuthSAName    = "nebari-llm-operator"
)

func defaultAuthModel(name string) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testAuthNamespace,
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
		APIKeysNamespace:    testAuthAPIKeysNS,
	}
}

func TestBuildAuthResources(t *testing.T) { //nolint:gocyclo // table-driven test
	t.Parallel()

	tests := []struct {
		name  string
		model *llmv1alpha1.LLMModel
		cfg   *config.OperatorConfig
		check func(t *testing.T, result *AuthResources, err error)
	}{
		{
			name:  "API key Secret: correct name and namespace",
			model: defaultAuthModel(testAuthModelName),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.APIKeySecret == nil {
					t.Fatal("expected APIKeySecret to be non-nil")
				}
				if result.APIKeySecret.Name != APIKeySecretName(testAuthModelName) {
					t.Errorf("expected name %q, got %q", APIKeySecretName(testAuthModelName), result.APIKeySecret.Name)
				}
				if result.APIKeySecret.Namespace != testAuthAPIKeysNS {
					t.Errorf("expected namespace %s, got %q", testAuthAPIKeysNS, result.APIKeySecret.Namespace)
				}
			},
		},
		{
			name:  "API key Secret: empty data and Opaque type",
			model: defaultAuthModel(testAuthModelName),
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
			model: defaultAuthModel(testAuthModelName),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.APIKeyMetadataCM == nil {
					t.Fatal("expected APIKeyMetadataCM to be non-nil")
				}
				if result.APIKeyMetadataCM.Name != APIKeyMetadataConfigMapName(testAuthModelName) {
					t.Errorf("expected name %q, got %q", APIKeyMetadataConfigMapName(testAuthModelName), result.APIKeyMetadataCM.Name)
				}
				if result.APIKeyMetadataCM.Namespace != testAuthAPIKeysNS {
					t.Errorf("expected namespace %s, got %q", testAuthAPIKeysNS, result.APIKeyMetadataCM.Namespace)
				}
			},
		},
		{
			name:  "API key ConfigMap: empty data",
			model: defaultAuthModel(testAuthModelName),
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
			model: defaultAuthModel(testAuthModelName),
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
					if resource.labels["llm.nebari.dev/model-name"] != testAuthModelName {
						t.Errorf("%s: expected model-name label %s, got %q", resource.name, testAuthModelName, resource.labels["llm.nebari.dev/model-name"])
					}
					if resource.labels["llm.nebari.dev/model-namespace"] != testAuthNamespace {
						t.Errorf("%s: expected model-namespace label %s, got %q", resource.name, testAuthNamespace, resource.labels["llm.nebari.dev/model-namespace"])
					}
					if resource.labels["app.kubernetes.io/managed-by"] != testAuthSAName {
						t.Errorf("%s: expected managed-by label %s, got %q", resource.name, testAuthSAName, resource.labels["app.kubernetes.io/managed-by"])
					}
				}
			},
		},
		{
			name:  "ReferenceGrant: correct apiVersion and kind",
			model: defaultAuthModel(testAuthModelName),
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
			model: defaultAuthModel(testAuthModelName),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ReferenceGrant.GetNamespace() != testAuthAPIKeysNS {
					t.Errorf("expected namespace %s, got %q", testAuthAPIKeysNS, result.ReferenceGrant.GetNamespace())
				}
			},
		},
		{
			name:  "ReferenceGrant: name includes both model name and namespace for uniqueness",
			model: defaultAuthModel(testAuthModelName),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				expectedName := testAuthModelName + "-" + testAuthNamespace + "-ref-grant"
				if result.ReferenceGrant.GetName() != expectedName {
					t.Errorf("expected name %q, got %q", expectedName, result.ReferenceGrant.GetName())
				}
			},
		},
		{
			name:  "ReferenceGrant: correct from (SecurityPolicy in model namespace)",
			model: defaultAuthModel(testAuthModelName),
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
				if fromEntry["namespace"] != testAuthNamespace {
					t.Errorf("expected from namespace %s, got %q", testAuthNamespace, fromEntry["namespace"])
				}
			},
		},
		{
			name:  "ReferenceGrant: correct to (Secret by name in api-keys namespace)",
			model: defaultAuthModel(testAuthModelName),
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
				if toEntry["name"] != APIKeySecretName(testAuthModelName) {
					t.Errorf("expected to name %q, got %q", APIKeySecretName(testAuthModelName), toEntry["name"])
				}
			},
		},
		{
			name:  "External SecurityPolicy: correct targetRef to HTTPRoute <name>-external",
			model: defaultAuthModel(testAuthModelName),
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
				if targetRef["name"] != testAuthModelName+"-external" {
					t.Errorf("expected name %s-external, got %q", testAuthModelName, targetRef["name"])
				}
			},
		},
		{
			name:  "External SecurityPolicy: apiKeyAuth credentialRef to Secret in api-keys namespace",
			model: defaultAuthModel(testAuthModelName),
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
				if credRef["name"] != APIKeySecretName(testAuthModelName) {
					t.Errorf("expected name %q, got %q", APIKeySecretName(testAuthModelName), credRef["name"])
				}
				if credRef["namespace"] != testAuthAPIKeysNS {
					t.Errorf("expected namespace %s, got %q", testAuthAPIKeysNS, credRef["namespace"])
				}
			},
		},
		// NOTE: sanitize and forwardClientIDHeader are Envoy Gateway v1.7+ features.
		// Tests for these fields will be added when minimum EG version is bumped.
		{
			name:  "External SecurityPolicy: extractFrom headers includes Authorization",
			model: defaultAuthModel(testAuthModelName),
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
				m := defaultAuthModel(testAuthModelName)
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
			model: defaultAuthModel(testAuthModelName),
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
				if targetRef["name"] != testAuthModelName+"-internal" {
					t.Errorf("expected name %s-internal, got %q", testAuthModelName, targetRef["name"])
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: JWT provider with correct issuer and JWKS URI",
			model: defaultAuthModel(testAuthModelName),
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
			model: defaultAuthModel(testAuthModelName),
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
			model: defaultAuthModel(testAuthModelName),
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
			model: defaultAuthModel(testAuthModelName),
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
				m := defaultAuthModel(testAuthModelName)
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
				m := defaultAuthModel(testAuthModelName)
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
			model: defaultAuthModel(testAuthModelName),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalSecurityPolicy.GetName() != testAuthModelName+"-external-auth" {
					t.Errorf("expected name %s-external-auth, got %q", testAuthModelName, result.ExternalSecurityPolicy.GetName())
				}
				if result.ExternalSecurityPolicy.GetNamespace() != testAuthNamespace {
					t.Errorf("expected namespace %s, got %q", testAuthNamespace, result.ExternalSecurityPolicy.GetNamespace())
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: correct name and namespace",
			model: defaultAuthModel(testAuthModelName),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InternalSecurityPolicy.GetName() != testAuthModelName+"-internal-auth" {
					t.Errorf("expected name %s-internal-auth, got %q", testAuthModelName, result.InternalSecurityPolicy.GetName())
				}
				if result.InternalSecurityPolicy.GetNamespace() != testAuthNamespace {
					t.Errorf("expected namespace %s, got %q", testAuthNamespace, result.InternalSecurityPolicy.GetNamespace())
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
