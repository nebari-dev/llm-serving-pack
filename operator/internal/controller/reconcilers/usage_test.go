package reconcilers

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// capturedTokenCosts returns the token counts a route asks the Envoy AI
// Gateway to record per request, as cost type -> dynamic-metadata key.
func capturedTokenCosts(t *testing.T, route *unstructured.Unstructured) map[string]string {
	t.Helper()
	if route == nil {
		t.Fatal("expected route to be non-nil")
	}
	costs, _, err := unstructured.NestedSlice(route.Object, "spec", "llmRequestCosts")
	if err != nil {
		t.Fatalf("reading spec.llmRequestCosts: %v", err)
	}
	captured := make(map[string]string, len(costs))
	for _, c := range costs {
		cost, ok := c.(map[string]interface{})
		if !ok {
			t.Fatalf("expected llmRequestCosts entry to be a map, got %T", c)
		}
		costType, _ := cost["type"].(string)
		key, _ := cost["metadataKey"].(string)
		captured[costType] = key
	}
	return captured
}

// Usage metering reads these keys from the gateway access log, so every route
// the operator generates has to ask the gateway to record them.
func assertCapturesTokenUsage(t *testing.T, name string, route *unstructured.Unstructured) {
	t.Helper()
	want := map[string]string{
		"InputToken":              "llm_input_token",
		"CachedInputToken":        "llm_cached_input_token",
		"CacheCreationInputToken": "llm_cache_creation_input_token",
		"OutputToken":             "llm_output_token",
		"ReasoningToken":          "llm_reasoning_token",
		"TotalToken":              "llm_total_token",
	}
	got := capturedTokenCosts(t, route)
	for costType, key := range want {
		if got[costType] != key {
			t.Errorf("%s route: %s tokens recorded under key %q, want %q", name, costType, got[costType], key)
		}
	}
}

func TestServedModelRoutesCaptureTokenUsage(t *testing.T) {
	res, err := BuildRoutingResources(defaultRoutingModel(), defaultRoutingConfig())
	if err != nil {
		t.Fatalf("BuildRoutingResources: %v", err)
	}
	assertCapturesTokenUsage(t, "external", res.ExternalRoute)
	assertCapturesTokenUsage(t, "internal", res.InternalRoute)
}

func TestPassthroughRoutesCaptureTokenUsage(t *testing.T) {
	res, err := BuildPassthroughResources(testPassthroughModel(), testPassthroughConfig(), nil, nil)
	if err != nil {
		t.Fatalf("BuildPassthroughResources: %v", err)
	}
	assertCapturesTokenUsage(t, "external", res.ExternalRoute)
	assertCapturesTokenUsage(t, "internal", res.InternalRoute)
}

// jwtClaimHeaders returns the header -> JWT claim mapping an internal
// SecurityPolicy forwards upstream (and so makes visible to the access log).
func jwtClaimHeaders(t *testing.T, policy *unstructured.Unstructured) map[string]string {
	t.Helper()
	if policy == nil {
		t.Fatal("expected internal SecurityPolicy to be non-nil")
	}
	providers, _, err := unstructured.NestedSlice(policy.Object, "spec", "jwt", "providers")
	if err != nil || len(providers) == 0 {
		t.Fatalf("expected a JWT provider, got %v (err %v)", providers, err)
	}
	provider, _ := providers[0].(map[string]interface{})
	entries, _ := provider["claimToHeaders"].([]interface{})
	headers := make(map[string]string, len(entries))
	for _, e := range entries {
		entry, _ := e.(map[string]interface{})
		header, _ := entry["header"].(string)
		claim, _ := entry["claim"].(string)
		headers[header] = claim
	}
	return headers
}

// A username can be renamed and reused; the OIDC subject cannot. Usage records
// are attributed to the subject, so the internal (JWT) endpoint has to forward it.
func TestInternalEndpointsForwardTokenSubject(t *testing.T) {
	served, err := BuildAuthResources(defaultRoutingModel(), defaultRoutingConfig(), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthResources: %v", err)
	}
	passthrough, err := BuildPassthroughResources(testPassthroughModel(), testPassthroughConfig(), nil, nil)
	if err != nil {
		t.Fatalf("BuildPassthroughResources: %v", err)
	}
	for name, policy := range map[string]*unstructured.Unstructured{
		"served model": served.InternalSecurityPolicy,
		"passthrough":  passthrough.InternalSecurityPolicy,
	} {
		if got := jwtClaimHeaders(t, policy)["X-Auth-Subject"]; got != "sub" {
			t.Errorf("%s: X-Auth-Subject carries claim %q, want %q", name, got, "sub")
		}
	}
}
