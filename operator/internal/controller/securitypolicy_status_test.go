package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

type policyFailureClient struct {
	client.Client
	missing  bool
	failName string
	failure  error
}

func (c *policyFailureClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.missing && obj.GetObjectKind().GroupVersionKind().Kind == "SecurityPolicy" {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "gateway.envoyproxy.io", Kind: "SecurityPolicy"}}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *policyFailureClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if obj.GetObjectKind().GroupVersionKind().Kind == "SecurityPolicy" && obj.GetName() == c.failName && c.failure != nil {
		return c.failure
	}
	return c.Client.Update(ctx, obj, opts...)
}

func newPolicyTestReconciler(t *testing.T) (*LLMModelReconciler, *policyFailureClient, *llmv1alpha1.LLMModel) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := llmv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	model := newMinimalModel("auth-retry")
	model.Finalizers = []string{finalizerName}
	model.Generation = 1
	model.Status.Phase = llmv1alpha1.PhaseStarting
	c := &policyFailureClient{Client: fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&llmv1alpha1.LLMModel{}, &appsv1.Deployment{}).
		WithObjects(model).Build()}
	return &LLMModelReconciler{Client: c, Scheme: s, Config: testConfig()}, c, model
}

func TestSecurityPolicyUpdateFailureAndRecovery(t *testing.T) {
	for _, endpoint := range []string{"external", "internal"} {
		for _, failureKind := range []string{"forbidden", "conflict"} {
			t.Run(endpoint+"/"+failureKind, func(t *testing.T) {
				ctx := context.Background()
				r, c, model := newPolicyTestReconciler(t)
				req := reconcileRequest(model.Name)
				keys := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: model.Name + "-api-keys", Namespace: model.Namespace}, Data: map[string][]byte{"revoked-client": []byte("sk-test")}}
				if err := c.Create(ctx, keys); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(ctx, req); err != nil {
					t.Fatal(err)
				}
				policy := endpointObject("gateway.envoyproxy.io", "v1alpha1", "SecurityPolicy", model.Namespace, model.Name+"-"+endpoint+"-auth")
				if err := c.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
					t.Fatal(err)
				}
				oldSpec := policy.DeepCopy().Object["spec"]
				// Revoke the key and change allowed groups on an already-ready model.
				if err := c.Get(ctx, client.ObjectKeyFromObject(model), model); err != nil {
					t.Fatal(err)
				}
				model.Status.Phase = llmv1alpha1.PhaseReady
				if err := c.Status().Update(ctx, model); err != nil {
					t.Fatal(err)
				}
				model.Spec.Access.Groups = []string{"new-group"}
				model.Generation++
				if err := c.Update(ctx, model); err != nil {
					t.Fatal(err)
				}
				if err := c.Get(ctx, client.ObjectKeyFromObject(keys), keys); err != nil {
					t.Fatal(err)
				}
				keys.Data = nil
				if err := c.Update(ctx, keys); err != nil {
					t.Fatal(err)
				}
				c.failName = policy.GetName()
				gr := schema.GroupResource{Group: "gateway.envoyproxy.io", Resource: "securitypolicies"}
				c.failure = apierrors.NewForbidden(gr, c.failName, errors.New("denied"))
				if failureKind == "conflict" {
					c.failure = apierrors.NewConflict(gr, c.failName, errors.New("changed"))
				}
				if _, err := r.Reconcile(ctx, req); !errors.Is(err, c.failure) {
					t.Fatalf("expected retryable error, got %v", err)
				}
				if err := c.Get(ctx, client.ObjectKeyFromObject(model), model); err != nil {
					t.Fatal(err)
				}
				cond := meta.FindStatusCondition(model.Status.Conditions, condSecurityPoliciesReady)
				if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "ApplyFailed" || cond.ObservedGeneration != model.Generation {
					t.Fatalf("failure not reported: %+v", cond)
				}
				if err := c.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(oldSpec, policy.Object["spec"]) {
					t.Fatal("failed update unexpectedly replaced policy")
				}
				c.failure = nil
				if _, err := r.Reconcile(ctx, req); err != nil {
					t.Fatal(err)
				}
				if err := c.Get(ctx, client.ObjectKeyFromObject(model), model); err != nil {
					t.Fatal(err)
				}
				cond = meta.FindStatusCondition(model.Status.Conditions, condSecurityPoliciesReady)
				if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "Applied" {
					t.Fatalf("recovery not reported: %+v", cond)
				}
				if err := c.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
					t.Fatal(err)
				}
				if reflect.DeepEqual(oldSpec, policy.Object["spec"]) {
					t.Fatal("retry did not replace stale policy")
				}
			})
		}
	}
}

func TestMissingSecurityPolicyCRDRetriesWithoutBlockingProvisioning(t *testing.T) {
	ctx := context.Background()
	r, c, model := newPolicyTestReconciler(t)
	c.missing = true
	req := reconcileRequest(model.Name)
	result, err := r.Reconcile(ctx, req)
	if err != nil || result.RequeueAfter != 15*time.Second {
		t.Fatalf("missing CRD retry = %+v, %v", result, err)
	}
	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(model), dep); err != nil {
		t.Fatal("provisioning blocked", err)
	}
	dep.Status.ReadyReplicas = 1
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatal(err)
	}
	result, err = r.Reconcile(ctx, req)
	if err != nil || result.RequeueAfter != 15*time.Second {
		t.Fatalf("ready model lost CRD retry = %+v, %v", result, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(model), model); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(model.Status.Conditions, condSecurityPoliciesReady)
	if cond == nil || cond.Reason != "GatewayCRDUnavailable" || cond.Status != metav1.ConditionFalse {
		t.Fatalf("missing CRD not reported: %+v", cond)
	}
	c.missing = false
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(model), model); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(model.Status.Conditions, condSecurityPoliciesReady) {
		t.Fatal("condition did not recover after CRD installation")
	}
}
