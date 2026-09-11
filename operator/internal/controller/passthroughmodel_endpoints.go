package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// removeEndpoint removes only this provider's route and authentication policy.
// Keep authentication until the AI Gateway-owned HTTPRoute is gone: removing
// the policy first could briefly expose the upstream without authentication.
func (r *PassthroughModelReconciler) removeEndpoint(ctx context.Context, pm *llmv1alpha1.PassthroughModel, endpoint string) (pending bool, err error) {
	name := pm.Name + "-" + endpoint
	route := endpointObject("aigateway.envoyproxy.io", "v1beta1", "AIGatewayRoute", pm.Namespace, name)
	if pending, err = r.removeControlledObject(ctx, pm, route); pending || err != nil {
		return pending, err
	}
	httpRoute := endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", pm.Namespace, name)
	if err := r.Get(ctx, client.ObjectKeyFromObject(httpRoute), httpRoute); err == nil {
		return true, nil // Wait for AI Gateway/Kubernetes to remove its generated route.
	} else if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return false, err
	}
	policy := endpointObject("gateway.envoyproxy.io", "v1alpha1", "SecurityPolicy", pm.Namespace, name+"-auth")
	return r.removeControlledObject(ctx, pm, policy)
}

func endpointObject(group, version, kind, namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: kind})
	obj.SetNamespace(namespace)
	obj.SetName(name)
	return obj
}

func (r *PassthroughModelReconciler) removeControlledObject(ctx context.Context, pm *llmv1alpha1.PassthroughModel, obj *unstructured.Unstructured) (bool, error) {
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, err
	}
	if !metav1.IsControlledBy(obj, pm) {
		return false, fmt.Errorf("refusing to remove %s %s owned outside this provider", obj.GetKind(), obj.GetName())
	}
	if obj.GetDeletionTimestamp() == nil {
		uid, rv := obj.GetUID(), obj.GetResourceVersion()
		if err := r.Delete(ctx, obj, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return true, nil
}
