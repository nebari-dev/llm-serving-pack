package reconcilers

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

func testPassthroughConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:          "example.com",
		ExternalGatewayName: "nebari-gateway",
		ExternalGatewayNS:   "envoy-gateway-system",
		InternalGatewayName: "nebari-gateway",
		InternalGatewayNS:   "envoy-gateway-system",
		OIDCIssuerURL:       "https://keycloak.example.com/realms/nebari",
		OIDCGroupsClaim:     "groups",
	}
}

func testPassthroughModel() *llmv1alpha1.PassthroughModel {
	return &llmv1alpha1.PassthroughModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openrouter",
			Namespace: "llm-system",
		},
		Spec: llmv1alpha1.PassthroughModelSpec{
			Provider: llmv1alpha1.ProviderSpec{
				Hostname:             "openrouter.ai",
				Port:                 443,
				SchemaVersion:        "api/v1",
				CredentialSecretName: "openrouter-api-key",
			},
			Models: llmv1alpha1.PassthroughModels{
				CatchAll: true,
				Declared: []string{"openai/gpt-5.2", "anthropic/claude-opus-4.6"},
			},
			Access: llmv1alpha1.AccessSpec{
				Groups: []string{"llm"},
			},
		},
	}
}

// specMap digs spec out of an unstructured or fails the test.
func specMap(t *testing.T, obj *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	if obj == nil {
		t.Fatal("expected object to be non-nil")
	}
	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected spec to be a map, got %T", obj.Object["spec"])
	}
	return spec
}

func routeRules(t *testing.T, route *unstructured.Unstructured) []interface{} {
	t.Helper()
	rules, ok := specMap(t, route)["rules"].([]interface{})
	if !ok {
		t.Fatalf("expected rules to be a slice, got %T", specMap(t, route)["rules"])
	}
	return rules
}

func TestBuildPassthroughResourcesProviderPlumbing(t *testing.T) {
	pm := testPassthroughModel()
	res, err := BuildPassthroughResources(pm, testPassthroughConfig())
	if err != nil {
		t.Fatalf("BuildPassthroughResources returned error: %v", err)
	}

	t.Run("backend", func(t *testing.T) {
		if res.Backend.GetName() != "openrouter-backend" {
			t.Errorf("backend name = %q, want openrouter-backend", res.Backend.GetName())
		}
		if res.Backend.GetNamespace() != "llm-system" {
			t.Errorf("backend namespace = %q", res.Backend.GetNamespace())
		}
		if res.Backend.GetAPIVersion() != "gateway.envoyproxy.io/v1alpha1" || res.Backend.GetKind() != "Backend" {
			t.Errorf("backend GVK = %s/%s", res.Backend.GetAPIVersion(), res.Backend.GetKind())
		}
		eps, _ := specMap(t, res.Backend)["endpoints"].([]interface{})
		if len(eps) != 1 {
			t.Fatalf("expected 1 endpoint, got %d", len(eps))
		}
		fqdn, _ := eps[0].(map[string]interface{})["fqdn"].(map[string]interface{})
		if fqdn["hostname"] != "openrouter.ai" {
			t.Errorf("fqdn hostname = %v", fqdn["hostname"])
		}
		if fqdn["port"] != int64(443) {
			t.Errorf("fqdn port = %v (%T)", fqdn["port"], fqdn["port"])
		}
	})

	t.Run("backend TLS policy", func(t *testing.T) {
		p := res.BackendTLSPolicy
		if p.GetName() != "openrouter-backend-tls" {
			t.Errorf("tls policy name = %q", p.GetName())
		}
		if p.GetAPIVersion() != "gateway.networking.k8s.io/v1alpha3" || p.GetKind() != "BackendTLSPolicy" {
			t.Errorf("tls policy GVK = %s/%s", p.GetAPIVersion(), p.GetKind())
		}
		validation, _ := specMap(t, p)["validation"].(map[string]interface{})
		if validation["hostname"] != "openrouter.ai" {
			t.Errorf("validation hostname = %v", validation["hostname"])
		}
		if validation["wellKnownCACertificates"] != "System" {
			t.Errorf("wellKnownCACertificates = %v", validation["wellKnownCACertificates"])
		}
		targets, _ := specMap(t, p)["targetRefs"].([]interface{})
		target, _ := targets[0].(map[string]interface{})
		if target["kind"] != "Backend" || target["name"] != "openrouter-backend" {
			t.Errorf("targetRef = %v", target)
		}
	})

	t.Run("AIServiceBackend", func(t *testing.T) {
		b := res.AIServiceBackend
		if b.GetName() != "openrouter" {
			t.Errorf("AIServiceBackend name = %q", b.GetName())
		}
		schema, _ := specMap(t, b)["schema"].(map[string]interface{})
		if schema["name"] != "OpenAI" || schema["version"] != "api/v1" {
			t.Errorf("schema = %v", schema)
		}
		ref, _ := specMap(t, b)["backendRef"].(map[string]interface{})
		if ref["kind"] != "Backend" || ref["name"] != "openrouter-backend" || ref["group"] != "gateway.envoyproxy.io" {
			t.Errorf("backendRef = %v", ref)
		}
	})

	t.Run("BackendSecurityPolicy", func(t *testing.T) {
		p := res.BackendSecurityPolicy
		if p.GetName() != "openrouter-upstream-auth" {
			t.Errorf("BSP name = %q", p.GetName())
		}
		if specMap(t, p)["type"] != "APIKey" {
			t.Errorf("type = %v", specMap(t, p)["type"])
		}
		apiKey, _ := specMap(t, p)["apiKey"].(map[string]interface{})
		ref, _ := apiKey["secretRef"].(map[string]interface{})
		if ref["name"] != "openrouter-api-key" {
			t.Errorf("secretRef = %v", ref)
		}
		targets, _ := specMap(t, p)["targetRefs"].([]interface{})
		target, _ := targets[0].(map[string]interface{})
		if target["kind"] != "AIServiceBackend" || target["name"] != "openrouter" {
			t.Errorf("targetRef = %v", target)
		}
	})
}

func TestBuildPassthroughResourcesKeySecretAndConfigMap(t *testing.T) {
	pm := testPassthroughModel()
	res, err := BuildPassthroughResources(pm, testPassthroughConfig())
	if err != nil {
		t.Fatalf("BuildPassthroughResources returned error: %v", err)
	}

	if res.APIKeySecret.Name != "openrouter-api-keys" {
		t.Errorf("secret name = %q", res.APIKeySecret.Name)
	}
	if res.APIKeySecret.Namespace != "llm-system" {
		t.Errorf("secret namespace = %q", res.APIKeySecret.Namespace)
	}
	if res.APIKeyMetadataCM.Name != "openrouter-api-key-metadata" {
		t.Errorf("configmap name = %q", res.APIKeyMetadataCM.Name)
	}
	// The key-manager filters API-key metadata ConfigMaps on this label.
	for _, obj := range []map[string]string{res.APIKeySecret.Labels, res.APIKeyMetadataCM.Labels} {
		if obj["llm.nebari.dev/model-name"] != "openrouter" {
			t.Errorf("model-name label = %q", obj["llm.nebari.dev/model-name"])
		}
		if obj["llm.nebari.dev/model"] != "openrouter" {
			t.Errorf("model label = %q", obj["llm.nebari.dev/model"])
		}
	}
}

func TestBuildPassthroughResourcesRoutes(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*llmv1alpha1.PassthroughModel)
		wantExternal  bool
		wantInternal  bool
		wantRuleCount int // rules on each present route
	}{
		{
			name:          "declared plus catch-all",
			mutate:        func(pm *llmv1alpha1.PassthroughModel) {},
			wantExternal:  true,
			wantInternal:  true,
			wantRuleCount: 2,
		},
		{
			name: "catch-all only",
			mutate: func(pm *llmv1alpha1.PassthroughModel) {
				pm.Spec.Models.Declared = nil
			},
			wantExternal:  true,
			wantInternal:  true,
			wantRuleCount: 1,
		},
		{
			name: "declared only",
			mutate: func(pm *llmv1alpha1.PassthroughModel) {
				pm.Spec.Models.CatchAll = false
			},
			wantExternal:  true,
			wantInternal:  true,
			wantRuleCount: 1,
		},
		{
			name: "external disabled",
			mutate: func(pm *llmv1alpha1.PassthroughModel) {
				pm.Spec.Endpoints.External.Enabled = ptr.To(false)
			},
			wantExternal:  false,
			wantInternal:  true,
			wantRuleCount: 2,
		},
		{
			name: "internal disabled",
			mutate: func(pm *llmv1alpha1.PassthroughModel) {
				pm.Spec.Endpoints.Internal.Enabled = ptr.To(false)
			},
			wantExternal:  true,
			wantInternal:  false,
			wantRuleCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := testPassthroughModel()
			tt.mutate(pm)
			res, err := BuildPassthroughResources(pm, testPassthroughConfig())
			if err != nil {
				t.Fatalf("BuildPassthroughResources returned error: %v", err)
			}
			if (res.ExternalRoute != nil) != tt.wantExternal {
				t.Fatalf("external route present = %v, want %v", res.ExternalRoute != nil, tt.wantExternal)
			}
			if (res.InternalRoute != nil) != tt.wantInternal {
				t.Fatalf("internal route present = %v, want %v", res.InternalRoute != nil, tt.wantInternal)
			}
			if (res.ExternalSecurityPolicy != nil) != tt.wantExternal {
				t.Fatalf("external policy present = %v, want %v", res.ExternalSecurityPolicy != nil, tt.wantExternal)
			}
			if (res.InternalSecurityPolicy != nil) != tt.wantInternal {
				t.Fatalf("internal policy present = %v, want %v", res.InternalSecurityPolicy != nil, tt.wantInternal)
			}
			if res.ExternalRoute != nil {
				if got := len(routeRules(t, res.ExternalRoute)); got != tt.wantRuleCount {
					t.Errorf("external rules = %d, want %d", got, tt.wantRuleCount)
				}
			}
			if res.InternalRoute != nil {
				if got := len(routeRules(t, res.InternalRoute)); got != tt.wantRuleCount {
					t.Errorf("internal rules = %d, want %d", got, tt.wantRuleCount)
				}
			}
		})
	}
}

func TestBuildPassthroughRouteDetails(t *testing.T) {
	pm := testPassthroughModel()
	cfg := testPassthroughConfig()
	res, err := BuildPassthroughResources(pm, cfg)
	if err != nil {
		t.Fatalf("BuildPassthroughResources returned error: %v", err)
	}

	t.Run("external parentRef and section", func(t *testing.T) {
		parents, _ := specMap(t, res.ExternalRoute)["parentRefs"].([]interface{})
		parent, _ := parents[0].(map[string]interface{})
		if parent["name"] != "nebari-gateway" || parent["namespace"] != "envoy-gateway-system" {
			t.Errorf("parentRef = %v", parent)
		}
		if parent["sectionName"] != ExternalHTTPSListenerName {
			t.Errorf("sectionName = %v", parent["sectionName"])
		}
	})

	t.Run("declared rule lists models and ownedBy on external only", func(t *testing.T) {
		extRules := routeRules(t, res.ExternalRoute)
		declared, _ := extRules[0].(map[string]interface{})
		if declared["modelsOwnedBy"] != "openrouter" {
			t.Errorf("external modelsOwnedBy = %v", declared["modelsOwnedBy"])
		}
		matches, _ := declared["matches"].([]interface{})
		if len(matches) != 2 {
			t.Fatalf("declared matches = %d, want 2", len(matches))
		}
		// Every declared match carries Host AND x-ai-eg-model exact headers.
		seen := map[string]bool{}
		for _, m := range matches {
			headers, _ := m.(map[string]interface{})["headers"].([]interface{})
			var hostOK bool
			for _, h := range headers {
				hm, _ := h.(map[string]interface{})
				switch hm["name"] {
				case "Host":
					if hm["value"] == "llm.example.com" && hm["type"] == "Exact" {
						hostOK = true
					}
				case "x-ai-eg-model":
					seen[hm["value"].(string)] = true
				}
			}
			if !hostOK {
				t.Errorf("declared match missing exact Host header: %v", m)
			}
		}
		if !seen["openai/gpt-5.2"] || !seen["anthropic/claude-opus-4.6"] {
			t.Errorf("declared model ids = %v", seen)
		}

		intRules := routeRules(t, res.InternalRoute)
		intDeclared, _ := intRules[0].(map[string]interface{})
		if _, has := intDeclared["modelsOwnedBy"]; has {
			t.Errorf("internal route must not set modelsOwnedBy (avoids /v1/models duplicates)")
		}
	})

	t.Run("catch-all rule matches host only", func(t *testing.T) {
		extRules := routeRules(t, res.ExternalRoute)
		catchAll, _ := extRules[1].(map[string]interface{})
		matches, _ := catchAll["matches"].([]interface{})
		headers, _ := matches[0].(map[string]interface{})["headers"].([]interface{})
		if len(headers) != 1 {
			t.Fatalf("catch-all headers = %d, want 1 (Host only)", len(headers))
		}
		hm, _ := headers[0].(map[string]interface{})
		if hm["name"] != "Host" || hm["value"] != "llm.example.com" {
			t.Errorf("catch-all host header = %v", hm)
		}
		intRules := routeRules(t, res.InternalRoute)
		intCatchAll, _ := intRules[1].(map[string]interface{})
		intMatches, _ := intCatchAll["matches"].([]interface{})
		intHeaders, _ := intMatches[0].(map[string]interface{})["headers"].([]interface{})
		ihm, _ := intHeaders[0].(map[string]interface{})
		if ihm["value"] != "llm-internal.example.com" {
			t.Errorf("internal catch-all host = %v", ihm["value"])
		}
	})

	t.Run("rules reference the AIServiceBackend", func(t *testing.T) {
		for _, rule := range routeRules(t, res.ExternalRoute) {
			refs, _ := rule.(map[string]interface{})["backendRefs"].([]interface{})
			ref, _ := refs[0].(map[string]interface{})
			if ref["name"] != "openrouter" {
				t.Errorf("backendRef = %v", ref)
			}
		}
	})
}

func TestBuildPassthroughResourcesSecurityPolicies(t *testing.T) {
	pm := testPassthroughModel()
	cfg := testPassthroughConfig()
	res, err := BuildPassthroughResources(pm, cfg)
	if err != nil {
		t.Fatalf("BuildPassthroughResources returned error: %v", err)
	}

	t.Run("external apiKeyAuth", func(t *testing.T) {
		p := res.ExternalSecurityPolicy
		if p.GetName() != "openrouter-external-auth" {
			t.Errorf("name = %q", p.GetName())
		}
		targets, _ := specMap(t, p)["targetRefs"].([]interface{})
		target, _ := targets[0].(map[string]interface{})
		if target["kind"] != "HTTPRoute" || target["name"] != "openrouter-external" {
			t.Errorf("targetRef = %v", target)
		}
		auth, _ := specMap(t, p)["apiKeyAuth"].(map[string]interface{})
		creds, _ := auth["credentialRefs"].([]interface{})
		cred, _ := creds[0].(map[string]interface{})
		if cred["name"] != "openrouter-api-keys" {
			t.Errorf("credentialRef = %v", cred)
		}
	})

	t.Run("internal JWT with group authorization", func(t *testing.T) {
		p := res.InternalSecurityPolicy
		if p.GetName() != "openrouter-internal-auth" {
			t.Errorf("name = %q", p.GetName())
		}
		targets, _ := specMap(t, p)["targetRefs"].([]interface{})
		target, _ := targets[0].(map[string]interface{})
		if target["name"] != "openrouter-internal" {
			t.Errorf("targetRef = %v", target)
		}
		jwt, _ := specMap(t, p)["jwt"].(map[string]interface{})
		providers, _ := jwt["providers"].([]interface{})
		provider, _ := providers[0].(map[string]interface{})
		if provider["issuer"] != cfg.OIDCIssuerURL {
			t.Errorf("issuer = %v", provider["issuer"])
		}
		authz, ok := specMap(t, p)["authorization"].(map[string]interface{})
		if !ok {
			t.Fatal("expected authorization block for non-public access")
		}
		if authz["defaultAction"] != "Deny" {
			t.Errorf("defaultAction = %v", authz["defaultAction"])
		}
	})

	t.Run("public access drops authorization", func(t *testing.T) {
		pub := testPassthroughModel()
		pub.Spec.Access = llmv1alpha1.AccessSpec{Public: ptr.To(true)}
		pubRes, err := BuildPassthroughResources(pub, cfg)
		if err != nil {
			t.Fatalf("BuildPassthroughResources returned error: %v", err)
		}
		if _, has := specMap(t, pubRes.InternalSecurityPolicy)["authorization"]; has {
			t.Error("public access must not render an authorization block")
		}
	})
}
