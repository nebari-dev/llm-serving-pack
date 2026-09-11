package controller

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGatewayUpdatesPreserveUpstreamFinalizers(t *testing.T) {
	for _, provider := range []bool{false, true} {
		name := "served"
		if provider {
			name = "provider"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c := fake.NewClientBuilder().Build()
			var apply func(context.Context, *unstructured.Unstructured) error
			if provider {
				apply = (&PassthroughModelReconciler{Client: c}).createOrUpdateUnstructured
			} else {
				apply = (&LLMModelReconciler{Client: c}).createOrUpdateUnstructured
			}
			desired := endpointObject("aigateway.envoyproxy.io", "v1beta1", "AIGatewayRoute", "default", "model-external")
			desired.Object["spec"] = map[string]interface{}{"hostnames": []interface{}{"old.example.com"}}
			if err := apply(ctx, desired.DeepCopy()); err != nil {
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
				if err := apply(ctx, desired.DeepCopy()); err != nil {
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
		})
	}
}
