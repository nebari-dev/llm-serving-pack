package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// createOrUpdateUnstructured is the one apply path for gateway-owned kinds in
// both model reconcilers, so fixes to it (like finalizer preservation) land in
// every controller at once.
func createOrUpdateUnstructured(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	err := c.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, obj)
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	// Upstream controllers own their cleanup finalizers, including catalog removal.
	obj.SetFinalizers(existing.GetFinalizers())
	return c.Update(ctx, obj)
}
