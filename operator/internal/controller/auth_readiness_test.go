package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func TestLLMModelKeepsCredentialPoolCurrentWhileStarting(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := llmv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	policyGVK := schema.GroupVersionKind{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "SecurityPolicy"}
	s.AddKnownTypeWithName(policyGVK, &unstructured.Unstructured{})
	model := newMinimalModel("starting-model")
	model.Finalizers = []string{finalizerName}
	model.Status.Phase = llmv1alpha1.PhaseStarting
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&llmv1alpha1.LLMModel{}, &appsv1.Deployment{}).
		WithObjects(model).Build()
	r := &LLMModelReconciler{Client: c, Scheme: s, Config: testConfig()}
	if _, err := r.Reconcile(ctx, reconcileRequest(model.Name)); err != nil {
		t.Fatal(err)
	}

	// A provider added while a served model is loading must authenticate on
	// every route sharing the listener, even before ext_proc selects its route.
	providerKeys := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "new-provider-api-keys", Namespace: model.Namespace,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "nebari-llm-operator"},
	}}
	if err := c.Create(ctx, providerKeys); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcileRequest(model.Name)); err != nil {
		t.Fatal(err)
	}
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(policyGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: model.Name + "-external-auth", Namespace: model.Namespace}, policy); err != nil {
		t.Fatal(err)
	}
	refs, found, err := unstructured.NestedSlice(policy.Object, "spec", "apiKeyAuth", "credentialRefs")
	if err != nil || !found || len(refs) != 2 {
		t.Fatalf("expected both credential Secrets while starting, got %v, err=%v", refs, err)
	}
	if refs[0].(map[string]interface{})["name"] != providerKeys.Name {
		t.Fatalf("provider Secret absent from pooled credentials: %v", refs)
	}
	fetched := &llmv1alpha1.LLMModel{}
	if err := c.Get(ctx, types.NamespacedName{Name: model.Name, Namespace: model.Namespace}, fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.Status.Phase != llmv1alpha1.PhaseStarting {
		t.Fatalf("expected unready model, got %s", fetched.Status.Phase)
	}
}
