package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// A PassthroughModel can switch between the gateway's public roots and a CA
// ConfigMap in either direction. Gateway API rejects a BackendTLSPolicy that
// names both trust sources, so the stored policy must hold exactly one after
// every switch, through the same build and apply path the reconciler uses.
func TestBackendTLSPolicyTrustSourceSwitchesBothWays(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	pm := newPassthroughModel("self-hosted", "default")
	pm.Spec.Provider.Hostname = "llm.corp.example.com"
	ca := &llmv1alpha1.ProviderTLS{CAConfigMapName: "corp-ca"}

	for i, tls := range []*llmv1alpha1.ProviderTLS{nil, ca, nil, ca} {
		pm.Spec.Provider.TLS = tls
		res, err := reconcilers.BuildPassthroughResources(pm, testConfig(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyModelResourcePreservingFinalizers(ctx, c, res.BackendTLSPolicy.DeepCopy()); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		stored := res.BackendTLSPolicy.DeepCopy()
		if err := c.Get(ctx, client.ObjectKeyFromObject(stored), stored); err != nil {
			t.Fatal(err)
		}
		validation, _, err := unstructured.NestedMap(stored.Object, "spec", "validation")
		if err != nil {
			t.Fatal(err)
		}
		if validation["hostname"] != "llm.corp.example.com" {
			t.Errorf("step %d: hostname = %v", i, validation["hostname"])
		}
		refs, hasRefs := validation["caCertificateRefs"].([]interface{})
		system, hasSystem := validation["wellKnownCACertificates"]
		if tls == nil {
			if hasRefs || system != "System" {
				t.Fatalf("step %d: want only the system roots, got %v", i, validation)
			}
			continue
		}
		if hasSystem || len(refs) != 1 || refs[0].(map[string]interface{})["name"] != "corp-ca" {
			t.Fatalf("step %d: want only the corp-ca ConfigMap, got %v", i, validation)
		}
	}
}
