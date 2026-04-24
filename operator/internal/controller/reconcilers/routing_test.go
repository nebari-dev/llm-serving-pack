package reconcilers

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

// modelHeaderMatch extracts the `x-ai-eg-model` header matcher from the first
// rule of the given AIGatewayRoute. It fatally fails the test if the structure
// does not match the operator's expected layout.
func modelHeaderMatch(t *testing.T, route *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	if route == nil {
		t.Fatal("expected route to be non-nil")
	}
	spec, ok := route.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected spec to be a map, got %T", route.Object["spec"])
	}
	rules, ok := spec["rules"].([]interface{})
	if !ok || len(rules) == 0 {
		t.Fatalf("expected at least one rule, got %v", spec["rules"])
	}
	rule, ok := rules[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected rule to be a map, got %T", rules[0])
	}
	matches, ok := rule["matches"].([]interface{})
	if !ok || len(matches) == 0 {
		t.Fatalf("expected at least one match on rule, got %v", rule["matches"])
	}
	match, ok := matches[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected match to be a map, got %T", matches[0])
	}
	headers, ok := match["headers"].([]interface{})
	if !ok || len(headers) == 0 {
		t.Fatalf("expected at least one header matcher, got %v", match["headers"])
	}
	for _, h := range headers {
		header, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		if header["name"] == "x-ai-eg-model" {
			return header
		}
	}
	t.Fatalf("did not find an x-ai-eg-model header matcher in %v", headers)
	return nil
}

// modelHeaderMatchValue returns the value of the x-ai-eg-model header matcher.
func modelHeaderMatchValue(t *testing.T, route *unstructured.Unstructured) string {
	t.Helper()
	header := modelHeaderMatch(t, route)
	v, ok := header["value"].(string)
	if !ok {
		t.Fatalf("expected x-ai-eg-model header value to be a string, got %T", header["value"])
	}
	return v
}

func defaultRoutingModel() *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAuthModelName,
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
		DefaultServingImage: "ghcr.io/llm-d/llm-d-cuda:v0.6.0",
	}
}

func TestBuildRoutingResources(t *testing.T) { //nolint:gocyclo // table-driven test
	t.Parallel()

	const wantModelName = "mistralai/Mistral-7B-v0.1"

	tests := []struct {
		name  string
		model *llmv1alpha1.LLMModel
		cfg   *config.OperatorConfig
		check func(t *testing.T, result *RoutingResources, err error)
	}{
		{
			name:  "External enabled: correct apiVersion, kind, name suffix -external",
			model: defaultRoutingModel(),
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
		{
			name:  "External: rule has x-ai-eg-model header match equal to spec.model.name",
			model: defaultRoutingModel(),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				got := modelHeaderMatchValue(t, result.ExternalRoute)
				if got != wantModelName {
					t.Errorf("expected external x-ai-eg-model match %q, got %q", wantModelName, got)
				}
			},
		},
		{
			name:  "External: parentRef points to external gateway from config",
			model: defaultRoutingModel(),
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
			model: defaultRoutingModel(),
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
				if backendRef["name"] != testAuthModelName {
					t.Errorf("expected backendRef name my-model, got %q", backendRef["name"])
				}
			},
		},
		{
			name: "External disabled (enabled=false): ExternalRoute is nil",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultRoutingModel()
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
			model: defaultRoutingModel(),
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
		{
			name:  "Internal: rule has x-ai-eg-model header match equal to spec.model.name",
			model: defaultRoutingModel(),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				got := modelHeaderMatchValue(t, result.InternalRoute)
				if got != wantModelName {
					t.Errorf("expected internal x-ai-eg-model match %q, got %q", wantModelName, got)
				}
			},
		},
		{
			name: "External and internal: subdomain on spec does not change matcher value",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultRoutingModel()
				m.Spec.Endpoints.External.Subdomain = "custom-sub"
				return m
			}(),
			cfg: defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := modelHeaderMatchValue(t, result.ExternalRoute); got != wantModelName {
					t.Errorf("expected external x-ai-eg-model match %q (subdomain ignored at routing layer), got %q", wantModelName, got)
				}
				if got := modelHeaderMatchValue(t, result.InternalRoute); got != wantModelName {
					t.Errorf("expected internal x-ai-eg-model match %q (subdomain ignored at routing layer), got %q", wantModelName, got)
				}
			},
		},
		{
			name:  "Both routes: x-ai-eg-model matches use Exact type",
			model: defaultRoutingModel(),
			cfg:   defaultRoutingConfig(),
			check: func(t *testing.T, result *RoutingResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				for _, route := range []struct {
					name string
					r    *unstructured.Unstructured
				}{
					{"external", result.ExternalRoute},
					{"internal", result.InternalRoute},
				} {
					header := modelHeaderMatch(t, route.r)
					if header["type"] != "Exact" {
						t.Errorf("%s: expected x-ai-eg-model match type Exact, got %v", route.name, header["type"])
					}
					if header["name"] != "x-ai-eg-model" {
						t.Errorf("%s: expected header name x-ai-eg-model, got %v", route.name, header["name"])
					}
				}
			},
		},
		{
			name:  "Internal: parentRef points to internal gateway from config",
			model: defaultRoutingModel(),
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
			model: defaultRoutingModel(),
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
				if backendRef["name"] != testAuthModelName {
					t.Errorf("expected backendRef name my-model, got %q", backendRef["name"])
				}
			},
		},
		{
			name: "Internal disabled (enabled=false): InternalRoute is nil",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultRoutingModel()
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
				m := defaultRoutingModel()
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
			model: defaultRoutingModel(),
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
					if labels["app.kubernetes.io/instance"] != testAuthModelName {
						t.Errorf("%s: expected instance label my-model, got %q", route.name, labels["app.kubernetes.io/instance"])
					}
					if labels["llm.nebari.dev/model"] != testAuthModelName {
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
