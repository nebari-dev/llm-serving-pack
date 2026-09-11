package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func TestDisabledProviderEndpointKeepsAuthUntilRouteIsGone(t *testing.T) {
	for _, endpoint := range []string{"external", "internal"} {
		t.Run(endpoint, func(t *testing.T) {
			ctx := context.Background()
			s := runtime.NewScheme()
			if err := llmv1alpha1.AddToScheme(s); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(s); err != nil {
				t.Fatal(err)
			}
			pm := newPassthroughModel("provider", "default")
			pm.UID = "provider-uid"
			name := pm.Name + "-" + endpoint
			route := endpointObject("aigateway.envoyproxy.io", "v1beta1", "AIGatewayRoute", pm.Namespace, name)
			policy := endpointObject("gateway.envoyproxy.io", "v1alpha1", "SecurityPolicy", pm.Namespace, name+"-auth")
			httpRoute := endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", pm.Namespace, name)
			for _, obj := range []client.Object{route, policy} {
				if err := controllerutil.SetControllerReference(pm, obj, s); err != nil {
					t.Fatal(err)
				}
			}
			keys := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pm.Name + "-api-keys", Namespace: pm.Namespace}, Data: map[string][]byte{"user": []byte("keep")}}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(route, policy, httpRoute, keys).Build()
			r := &PassthroughModelReconciler{Client: c}
			if pending, err := r.removeEndpoint(ctx, pm, endpoint); err != nil || !pending {
				t.Fatalf("first cleanup = %v, %v", pending, err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(route), route); !apierrors.IsNotFound(err) {
				t.Fatalf("route still exists: %v", err)
			}
			if pending, err := r.removeEndpoint(ctx, pm, endpoint); err != nil || !pending {
				t.Fatalf("waiting for generated route = %v, %v", pending, err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
				t.Fatal("authentication removed before route", err)
			}
			// Fake clients have no garbage collector. Simulate its deletion.
			if err := c.Delete(ctx, httpRoute); err != nil {
				t.Fatal(err)
			}
			if _, err := r.removeEndpoint(ctx, pm, endpoint); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(policy), policy); !apierrors.IsNotFound(err) {
				t.Fatalf("policy still exists: %v", err)
			}
			if pending, err := r.removeEndpoint(ctx, pm, endpoint); err != nil || pending {
				t.Fatalf("idempotent cleanup = %v, %v", pending, err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(keys), keys); err != nil || string(keys.Data["user"]) != "keep" {
				t.Fatalf("user key changed: %v", err)
			}
		})
	}
}

func TestDisabledProviderEndpointPreservesForeignRoute(t *testing.T) {
	pm := newPassthroughModel("provider", "default")
	pm.UID = "provider-uid"
	route := endpointObject("aigateway.envoyproxy.io", "v1beta1", "AIGatewayRoute", pm.Namespace, pm.Name+"-external")
	c := fake.NewClientBuilder().WithObjects(route).Build()
	r := &PassthroughModelReconciler{Client: c}
	if _, err := r.removeEndpoint(context.Background(), pm, "external"); err == nil {
		t.Fatal("expected ownership error")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(route), route); err != nil {
		t.Fatal("foreign route removed", err)
	}
}
