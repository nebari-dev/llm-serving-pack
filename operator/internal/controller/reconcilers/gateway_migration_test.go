package reconcilers

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGatewayRoutesScopeCatalogsByEndpoint(t *testing.T) {
	pm := testPassthroughModel()
	provider, err := BuildPassthroughResources(pm, testPassthroughConfig(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	served, err := BuildRoutingResources(defaultRoutingModel(), defaultRoutingConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		route *unstructured.Unstructured
		host  string
	}{
		{provider.ExternalRoute, "llm.example.com"},
		{provider.InternalRoute, "llm-internal.example.com"},
		{served.ExternalRoute, "llm.example.com"},
		{served.InternalRoute, "llm-internal.example.com"},
	} {
		if tc.route.GetAPIVersion() != aiGatewayAPIVersion {
			t.Errorf("%s uses a deprecated API", tc.route.GetName())
		}
		hosts, _, err := unstructured.NestedStringSlice(tc.route.Object, "spec", "hostnames")
		if err != nil || len(hosts) != 1 || hosts[0] != tc.host {
			t.Errorf("%s hostnames = %v, %v", tc.route.GetName(), hosts, err)
		}
	}
	for _, resource := range []*unstructured.Unstructured{provider.AIServiceBackend, provider.BackendSecurityPolicy} {
		if resource.GetAPIVersion() != aiGatewayAPIVersion {
			t.Errorf("%s uses a deprecated API", resource.GetKind())
		}
	}
}
