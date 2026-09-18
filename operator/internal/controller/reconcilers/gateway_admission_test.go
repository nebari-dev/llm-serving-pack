package reconcilers

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Run against the rendered upstream CRD chart, not a hand-maintained schema.
func TestGatewayAdmission(t *testing.T) {
	dir := os.Getenv("AI_GATEWAY_CRD_DIR")
	if dir == "" {
		t.Skip("set AI_GATEWAY_CRD_DIR to the rendered upstream AI Gateway CRDs")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{dir}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pm := testPassthroughModel()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pm.Namespace}}); err != nil {
		t.Fatal(err)
	}
	resources, err := BuildPassthroughResources(pm, testPassthroughConfig(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []*unstructured.Unstructured{
		resources.AIServiceBackend, resources.BackendSecurityPolicy,
		resources.ExternalRoute, resources.InternalRoute,
	} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("%s rejected by upstream CRD: %v", obj.GetKind(), err)
		}
	}
	// Unknown fields can be silently pruned by admission. Confirm the actual
	// stored prefix and hostnames, not only successful resource creation.
	backend := resources.AIServiceBackend.DeepCopy()
	if err := c.Get(ctx, client.ObjectKeyFromObject(backend), backend); err != nil {
		t.Fatal(err)
	}
	prefix, _, _ := unstructured.NestedString(backend.Object, "spec", "schema", "prefix")
	if prefix != "/api/v1" {
		t.Fatalf("stored prefix = %q", prefix)
	}
	for _, route := range []*unstructured.Unstructured{resources.ExternalRoute, resources.InternalRoute} {
		if err := c.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
			t.Fatal(err)
		}
		hosts, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
		if len(hosts) != 1 {
			t.Fatalf("stored hostnames = %v", hosts)
		}
	}
}
