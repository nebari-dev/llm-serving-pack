package controller

import (
	"context"
	"reflect"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Both model reconcilers apply gateway kinds through the one shared helper,
// so this test covers the served and the provider path alike.
func TestGatewayUpdatesPreserveUpstreamFinalizers(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	desired := endpointObject("aigateway.envoyproxy.io", "v1beta1", "AIGatewayRoute", "default", "model-external")
	desired.Object["spec"] = map[string]interface{}{"hostnames": []interface{}{"old.example.com"}}
	if err := createOrUpdateUnstructured(ctx, c, desired.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	current := desired.DeepCopy()
	key := client.ObjectKeyFromObject(current)
	if err := c.Get(ctx, key, current); err != nil {
		t.Fatal(err)
	}
	// Simulate finalizers installed by other controllers after creation.
	finalizers := []string{"aigateway.envoyproxy.io/finalizer", "other.example.com/cleanup"}
	current.SetFinalizers(finalizers)
	if err := c.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	desired.Object["spec"] = map[string]interface{}{"hostnames": []interface{}{"new.example.com"}}
	for range 2 {
		if err := createOrUpdateUnstructured(ctx, c, desired.DeepCopy()); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, key, current); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(current.GetFinalizers(), finalizers) {
			t.Fatalf("upstream finalizers replaced: %v", current.GetFinalizers())
		}
		if !reflect.DeepEqual(current.Object["spec"], desired.Object["spec"]) {
			t.Fatal("desired spec was not updated")
		}
	}
	if err := c.Delete(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, current); err != nil || current.GetDeletionTimestamp() == nil {
		t.Fatalf("deletion did not wait for upstream cleanup: %v", err)
	}
}
