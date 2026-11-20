package controller

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// routeFailureClient rejects AIGatewayRoute reads, so every route apply fails
// with an error that is not a missing CRD. SecurityPolicies still apply, which
// keeps the CRD-pending requeue out of the way.
type routeFailureClient struct {
	client.Client
	fail bool
}

func (c *routeFailureClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.fail && obj.GetObjectKind().GroupVersionKind().Kind == "AIGatewayRoute" {
		gr := schema.GroupResource{Group: "aigateway.envoyproxy.io", Resource: "aigatewayroutes"}
		return apierrors.NewForbidden(gr, key.Name, errors.New("denied"))
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestRouteApplyFailureDegradesAndRecovers(t *testing.T) {
	ctx := context.Background()
	r, policyClient, model := newPolicyTestReconciler(t)
	c := &routeFailureClient{Client: policyClient, fail: true}
	r.Client = c
	req := reconcileRequest(model.Name)

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(model), dep); err != nil {
		t.Fatal(err)
	}
	dep.Status.Replicas = 1
	dep.Status.ReadyReplicas = 1
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatal(err)
	}

	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("a failed route apply must not fail the reconcile: %v", err)
	}
	if result.RequeueAfter != applyFailedRequeue {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, applyFailedRequeue)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(model), model); err != nil {
		t.Fatal(err)
	}
	if model.Status.Phase != llmv1alpha1.PhaseDegraded {
		t.Errorf("Phase = %q, want %q", model.Status.Phase, llmv1alpha1.PhaseDegraded)
	}
	for _, condType := range []string{CondExternalEndpointReady, CondInternalEndpointReady} {
		cond := meta.FindStatusCondition(model.Status.Conditions, condType)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonApplyFailed {
			t.Errorf("%s = %+v, want False/%s", condType, cond, reasonApplyFailed)
		}
	}
	if cond := meta.FindStatusCondition(model.Status.Conditions, condSecurityPoliciesReady); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("SecurityPoliciesReady = %+v, want True", cond)
	}

	c.fail = false
	result, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("RequeueAfter after recovery = %v, want 0", result.RequeueAfter)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(model), model); err != nil {
		t.Fatal(err)
	}
	if model.Status.Phase != llmv1alpha1.PhaseReady {
		t.Errorf("Phase after recovery = %q, want %q", model.Status.Phase, llmv1alpha1.PhaseReady)
	}
	for _, condType := range []string{CondExternalEndpointReady, CondInternalEndpointReady, CondInferencePoolReady} {
		cond := meta.FindStatusCondition(model.Status.Conditions, condType)
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonApplied {
			t.Errorf("%s after recovery = %+v, want True/%s", condType, cond, reasonApplied)
		}
	}
}
