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
	testAuthSAName    = "nebari-llm-operator"
)

func defaultAuthModel() *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAuthModelName,
			Namespace: testAuthNamespace,
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   "mistralai/Mistral-7B-v0.1",
				Source: llmv1alpha1.ModelSourceHuggingFace,
			},
			// Default to a non-public model with one group so the internal
			// SecurityPolicy always has a valid authorization block; matches
			// the invariant the webhook enforces at admission time.
			Access: llmv1alpha1.AccessSpec{
				Groups: []string{"data-scientists"},
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
		// APIKeysNamespace is no longer used to PLACE Secrets (per #59).
		// Leaving it empty matches the post-#59 default and ensures the
		// pure-build tests are not entangled with the legacy-cleanup
		// branch.
		APIKeysNamespace: "",
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
			model: defaultAuthModel(),
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
				if result.APIKeySecret.Namespace != testAuthNamespace {
					t.Errorf("expected namespace %s, got %q", testAuthNamespace, result.APIKeySecret.Namespace)
				}
			},
		},
		{
			name:  "API key Secret: empty data and Opaque type",
			model: defaultAuthModel(),
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
			model: defaultAuthModel(),
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
				if result.APIKeyMetadataCM.Namespace != testAuthNamespace {
					t.Errorf("expected namespace %s, got %q", testAuthNamespace, result.APIKeyMetadataCM.Namespace)
				}
			},
		},
		{
			name:  "API key ConfigMap: empty data",
			model: defaultAuthModel(),
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
			name:  "Labels: Secret and ConfigMap include standard labels (managed-by)",
			model: defaultAuthModel(),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				// model-name / model-namespace labels were specific to the
				// pre-#59 cross-namespace pattern. Now Secret + CM live in
				// the model's own namespace, so namespace-of-origin doesn't
				// need to be encoded as a label.
				for _, resource := range []struct {
					name   string
					labels map[string]string
				}{
					{"Secret", result.APIKeySecret.Labels},
					{"ConfigMap", result.APIKeyMetadataCM.Labels},
				} {
					if resource.labels["app.kubernetes.io/managed-by"] != testAuthSAName {
						t.Errorf("%s: expected managed-by label %s, got %q", resource.name, testAuthSAName, resource.labels["app.kubernetes.io/managed-by"])
					}
				}
			},
		},
		{
			name:  "External SecurityPolicy: correct targetRef to HTTPRoute <name>-external",
			model: defaultAuthModel(),
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
			name:  "External SecurityPolicy: apiKeyAuth credentialRef is same-namespace (no namespace field)",
			model: defaultAuthModel(),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ExternalSecurityPolicy.Object["spec"].(map[string]any)
				apiKeyAuth := spec["apiKeyAuth"].(map[string]any)
				credentialRefs := apiKeyAuth["credentialRefs"].([]any)
				if len(credentialRefs) != 1 {
					t.Fatalf("expected 1 credentialRef, got %d", len(credentialRefs))
				}
				credRef := credentialRefs[0].(map[string]any)
				if credRef["group"] != "" {
					t.Errorf("expected group empty string, got %q", credRef["group"])
				}
				if credRef["kind"] != "Secret" {
					t.Errorf("expected kind Secret, got %q", credRef["kind"])
				}
				if credRef["name"] != APIKeySecretName(testAuthModelName) {
					t.Errorf("expected name %q, got %q", APIKeySecretName(testAuthModelName), credRef["name"])
				}
				// Per #59: Envoy Gateway's APIKeyAuth rejects any non-same-ns
				// credentialRef, so we deliberately leave `namespace` unset.
				if _, present := credRef["namespace"]; present {
					t.Errorf("expected no namespace field on credentialRef, got %q", credRef["namespace"])
				}
			},
		},
		{
			name:  "External SecurityPolicy: apiKeyAuth forwards clientID and sanitizes",
			model: defaultAuthModel(),
			cfg:   defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalSecurityPolicy == nil {
					t.Fatal("expected ExternalSecurityPolicy to be non-nil")
				}
				spec := result.ExternalSecurityPolicy.Object["spec"].(map[string]interface{})
				apiKeyAuth := spec["apiKeyAuth"].(map[string]interface{})
				if got := apiKeyAuth["forwardClientIDHeader"]; got != "x-client-id" {
					t.Errorf("expected forwardClientIDHeader=x-client-id, got %v", got)
				}
				if got := apiKeyAuth["sanitize"]; got != true {
					t.Errorf("expected sanitize=true, got %v", got)
				}
			},
		},
		// NOTE: forwardClientIDHeader and sanitize are now rendered on the external
		// SecurityPolicy. Both fields require Envoy Gateway v1.5.1+ (present in the
		// pack's pinned v1.6.2; absent in v1.3). Operators on an older Envoy Gateway
		// must upgrade or the fields are rejected/ignored by the SecurityPolicy CRD.
		{
			name:  "External SecurityPolicy: extractFrom headers includes Authorization",
			model: defaultAuthModel(),
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
				m := defaultAuthModel()
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
			model: defaultAuthModel(),
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
			model: defaultAuthModel(),
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
				expectedURI := "https://oidc.example.com/protocol/openid-connect/certs"
				if remoteJWKS["uri"] != expectedURI {
					t.Errorf("expected JWKS URI %q, got %q", expectedURI, remoteJWKS["uri"])
				}
			},
		},
		{
			name:  "Internal SecurityPolicy: audiences set when OIDCAudience is non-empty",
			model: defaultAuthModel(),
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
			model: defaultAuthModel(),
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
			model: defaultAuthModel(),
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
			name: "Internal SecurityPolicy: authorization allows only model's groups when not public",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultAuthModel()
				m.Spec.Access = llmv1alpha1.AccessSpec{
					Groups: []string{"ml-team", "platform"},
				}
				return m
			}(),
			cfg: defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				authz, ok := spec["authorization"].(map[string]interface{})
				if !ok {
					t.Fatal("expected authorization block on internal SecurityPolicy")
				}
				if authz["defaultAction"] != "Deny" {
					t.Errorf("expected defaultAction=Deny, got %v", authz["defaultAction"])
				}
				rules := authz["rules"].([]interface{})
				if len(rules) != 1 {
					t.Fatalf("expected 1 rule, got %d", len(rules))
				}
				rule := rules[0].(map[string]interface{})
				if rule["action"] != "Allow" {
					t.Errorf("expected action=Allow, got %v", rule["action"])
				}
				principal := rule["principal"].(map[string]interface{})
				jwtP := principal["jwt"].(map[string]interface{})
				if jwtP["provider"] != "oidc" {
					t.Errorf("expected provider=oidc, got %v", jwtP["provider"])
				}
				claims := jwtP["claims"].([]interface{})
				if len(claims) != 1 {
					t.Fatalf("expected 1 claim matcher, got %d", len(claims))
				}
				claim := claims[0].(map[string]interface{})
				if claim["name"] != "groups" {
					t.Errorf("expected claim name=groups, got %v", claim["name"])
				}
				if claim["valueType"] != "StringArray" {
					t.Errorf("expected valueType=StringArray, got %v", claim["valueType"])
				}
				values := claim["values"].([]interface{})
				if len(values) != 2 || values[0] != "ml-team" || values[1] != "platform" {
					t.Errorf("expected values=[ml-team platform], got %v", values)
				}
			},
		},
		{
			name: "Internal SecurityPolicy: public model has no authorization block",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultAuthModel()
				public := true
				m.Spec.Access = llmv1alpha1.AccessSpec{Public: &public}
				return m
			}(),
			cfg: defaultAuthConfig(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				if _, found := spec["authorization"]; found {
					t.Error("expected no authorization block for public model")
				}
			},
		},
		{
			name: "Internal SecurityPolicy: authorization uses configured groups claim name",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultAuthModel()
				m.Spec.Access = llmv1alpha1.AccessSpec{
					Groups: []string{"ops"},
				}
				return m
			}(),
			cfg: func() *config.OperatorConfig {
				c := defaultAuthConfig()
				c.OIDCGroupsClaim = "realm_access.roles"
				return c
			}(),
			check: func(t *testing.T, result *AuthResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalSecurityPolicy.Object["spec"].(map[string]interface{})
				authz := spec["authorization"].(map[string]interface{})
				rules := authz["rules"].([]interface{})
				claim := rules[0].(map[string]interface{})["principal"].(map[string]interface{})["jwt"].(map[string]interface{})["claims"].([]interface{})[0].(map[string]interface{})
				if claim["name"] != "realm_access.roles" {
					t.Errorf("expected configured claim name, got %v", claim["name"])
				}
			},
		},
		{
			name: "Internal disabled: InternalSecurityPolicy is nil",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultAuthModel()
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
				m := defaultAuthModel()
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
			},
		},
		{
			name:  "External SecurityPolicy: correct name and namespace",
			model: defaultAuthModel(),
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
			model: defaultAuthModel(),
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
