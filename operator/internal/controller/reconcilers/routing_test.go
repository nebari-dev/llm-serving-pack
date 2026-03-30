package reconcilers

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

func defaultRoutingModel(name string) *llmv1alpha1.LLMModel {
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

func defaultRoutingConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:          "example.com",
		ExternalGatewayName: "external-gw",
		ExternalGatewayNS:   "envoy-gateway-system",
		InternalGatewayName: "internal-gw",
		InternalGatewayNS:   "envoy-gateway-system",
		OIDCIssuerURL:       "https://oidc.example.com",
		DefaultServingImage: "ghcr.io/llm-d/llm-d-cuda:v0.5.1",
	}
}

func TestBuildRoutingResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model *llmv1alpha1.LLMModel
		cfg   *config.OperatorConfig
		check func(t *testing.T, result *RoutingResources, err error)
	}{
		{
			name:  "External enabled: correct apiVersion, kind, name suffix -external",
			model: defaultRoutingModel("my-model"),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalRoute == nil {
					t.Fatal("expected ExternalRoute to be non-nil")
				}
				r := result.ExternalRoute
				if r.GetAPIVersion() != "aigateway.envoyproxy.io/v1alpha1" {
					t.Errorf("expected apiVersion aigateway.envoyproxy.io/v1alpha1, got %q", r.GetAPIVersion())
				}
				if r.GetKind() != "AIGatewayRoute" {
					t.Errorf("expected kind AIGatewayRoute, got %q", r.GetKind())
				}
				if !strings.HasSuffix(r.GetName(), "-external") {
					t.Errorf("expected name to end with -external, got %q", r.GetName())
				}
				if r.GetName() != "my-model-external" {
					t.Errorf("expected name my-model-external, got %q", r.GetName())
				}
			},
		},
		// NOTE: AIGatewayRoute does not support hostnames directly.
		// Hostname-based routing will be addressed in a future version.
		{
			name:  "External: parentRef points to external gateway from config",
			model: defaultRoutingModel("my-model"),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ExternalRoute.Object["spec"].(map[string]interface{})
				parentRefs := spec["parentRefs"].([]interface{})
				if len(parentRefs) != 1 {
					t.Fatalf("expected 1 parentRef, got %d", len(parentRefs))
				}
				parentRef := parentRefs[0].(map[string]interface{})
				if parentRef["name"] != "external-gw" {
					t.Errorf("expected parentRef name external-gw, got %q", parentRef["name"])
				}
				if parentRef["namespace"] != "envoy-gateway-system" {
					t.Errorf("expected parentRef namespace envoy-gateway-system, got %q", parentRef["namespace"])
				}
			},
		},
		{
			name:  "External: backendRef references InferencePool with correct group/kind/name",
			model: defaultRoutingModel("my-model"),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.ExternalRoute.Object["spec"].(map[string]interface{})
				rules := spec["rules"].([]interface{})
				if len(rules) != 1 {
					t.Fatalf("expected 1 rule, got %d", len(rules))
				}
				rule := rules[0].(map[string]interface{})
				backendRefs := rule["backendRefs"].([]interface{})
				if len(backendRefs) != 1 {
					t.Fatalf("expected 1 backendRef, got %d", len(backendRefs))
				}
				backendRef := backendRefs[0].(map[string]interface{})
				if backendRef["group"] != "inference.networking.k8s.io" {
					t.Errorf("expected group inference.networking.k8s.io, got %q", backendRef["group"])
				}
				if backendRef["kind"] != "InferencePool" {
					t.Errorf("expected kind InferencePool, got %q", backendRef["kind"])
				}
				if backendRef["name"] != "my-model" {
					t.Errorf("expected backendRef name my-model, got %q", backendRef["name"])
				}
			},
		},
		// NOTE: hostname tests removed - AIGatewayRoute does not support hostnames.
		// Subdomain/hostname routing will be addressed via Gateway listener config.
		{
			name: "External disabled (enabled=false): ExternalRoute is nil",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultRoutingModel("my-model")
				m.Spec.Endpoints.External.Enabled = boolPtr(false)
				return m
			}(),
			cfg: defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalRoute != nil {
					t.Error("expected ExternalRoute to be nil when external disabled")
				}
			},
		},
		{
			name:  "Internal enabled: AIGatewayRoute with name suffix -internal",
			model: defaultRoutingModel("my-model"),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InternalRoute == nil {
					t.Fatal("expected InternalRoute to be non-nil")
				}
				r := result.InternalRoute
				if r.GetAPIVersion() != "aigateway.envoyproxy.io/v1alpha1" {
					t.Errorf("expected apiVersion aigateway.envoyproxy.io/v1alpha1, got %q", r.GetAPIVersion())
				}
				if r.GetKind() != "AIGatewayRoute" {
					t.Errorf("expected kind AIGatewayRoute, got %q", r.GetKind())
				}
				if !strings.HasSuffix(r.GetName(), "-internal") {
					t.Errorf("expected name to end with -internal, got %q", r.GetName())
				}
				if r.GetName() != "my-model-internal" {
					t.Errorf("expected name my-model-internal, got %q", r.GetName())
				}
			},
		},
		// NOTE: Internal hostname test removed - AIGatewayRoute does not support hostnames.
		{
			name:  "Internal: parentRef points to internal gateway from config",
			model: defaultRoutingModel("my-model"),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalRoute.Object["spec"].(map[string]interface{})
				parentRefs := spec["parentRefs"].([]interface{})
				if len(parentRefs) != 1 {
					t.Fatalf("expected 1 parentRef, got %d", len(parentRefs))
				}
				parentRef := parentRefs[0].(map[string]interface{})
				if parentRef["name"] != "internal-gw" {
					t.Errorf("expected parentRef name internal-gw, got %q", parentRef["name"])
				}
				if parentRef["namespace"] != "envoy-gateway-system" {
					t.Errorf("expected parentRef namespace envoy-gateway-system, got %q", parentRef["namespace"])
				}
			},
		},
		{
			name:  "Internal: backendRef references same InferencePool",
			model: defaultRoutingModel("my-model"),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.InternalRoute.Object["spec"].(map[string]interface{})
				rules := spec["rules"].([]interface{})
				if len(rules) != 1 {
					t.Fatalf("expected 1 rule, got %d", len(rules))
				}
				rule := rules[0].(map[string]interface{})
				backendRefs := rule["backendRefs"].([]interface{})
				if len(backendRefs) != 1 {
					t.Fatalf("expected 1 backendRef, got %d", len(backendRefs))
				}
				backendRef := backendRefs[0].(map[string]interface{})
				if backendRef["group"] != "inference.networking.k8s.io" {
					t.Errorf("expected group inference.networking.k8s.io, got %q", backendRef["group"])
				}
				if backendRef["kind"] != "InferencePool" {
					t.Errorf("expected kind InferencePool, got %q", backendRef["kind"])
				}
				if backendRef["name"] != "my-model" {
					t.Errorf("expected backendRef name my-model, got %q", backendRef["name"])
				}
			},
		},
		{
			name: "Internal disabled (enabled=false): InternalRoute is nil",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultRoutingModel("my-model")
				m.Spec.Endpoints.Internal.Enabled = boolPtr(false)
				return m
			}(),
			cfg: defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.InternalRoute != nil {
					t.Error("expected InternalRoute to be nil when internal disabled")
				}
			},
		},
		{
			name: "Both disabled: both routes nil, no error",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultRoutingModel("my-model")
				m.Spec.Endpoints.External.Enabled = boolPtr(false)
				m.Spec.Endpoints.Internal.Enabled = boolPtr(false)
				return m
			}(),
			cfg: defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.ExternalRoute != nil {
					t.Error("expected ExternalRoute to be nil when both disabled")
				}
				if result.InternalRoute != nil {
					t.Error("expected InternalRoute to be nil when both disabled")
				}
			},
		},
		{
			name:  "Labels: StandardLabels on both routes",
			model: defaultRoutingModel("my-model"),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				for _, route := range []*struct {
					name string
					r    interface{ GetLabels() map[string]string }
				}{
					{"external", result.ExternalRoute},
					{"internal", result.InternalRoute},
				} {
					labels := route.r.GetLabels()
					if labels["app.kubernetes.io/managed-by"] != "nebari-llm-operator" {
						t.Errorf("%s: expected managed-by label, got %q", route.name, labels["app.kubernetes.io/managed-by"])
					}
					if labels["app.kubernetes.io/instance"] != "my-model" {
						t.Errorf("%s: expected instance label my-model, got %q", route.name, labels["app.kubernetes.io/instance"])
					}
					if labels["llm.nebari.dev/model"] != "my-model" {
						t.Errorf("%s: expected model label my-model, got %q", route.name, labels["llm.nebari.dev/model"])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := BuildRoutingResources(tt.model, tt.cfg)
			tt.check(t, result, err)
		})
	}
}
