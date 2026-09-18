package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applyModelResourcePreservingFinalizers creates or replaces resources managed by
// the LLMModel and PassthroughModel reconcilers, retaining upstream finalizers.
// Unlike ClusterTLS's spec-only patch, this also replaces desired metadata.
func applyModelResourcePreservingFinalizers(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
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
