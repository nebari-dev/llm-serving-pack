package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

type credentialStatusTestClient struct {
	client.Client
	credentialKey   client.ObjectKey
	credentialReads int
	credentialError error
	backendError    error
}

func (c *credentialStatusTestClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Secret); ok && (key == c.credentialKey || key.Name == "") {
		c.credentialReads++
		if c.credentialError != nil {
			return c.credentialError
		}
	}
	if obj.GetObjectKind().GroupVersionKind().Kind == "Backend" && c.backendError != nil {
		return c.backendError
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func newCredentialStatusTestReconciler(t *testing.T, pm *llmv1alpha1.PassthroughModel, objects ...client.Object) (*PassthroughModelReconciler, *credentialStatusTestClient) {
	t.Helper()
	pm.UID = "credential-status-test"
	pm.Generation = 1
	pm.Finalizers = []string{finalizerName}
	scheme := apikeysTestScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := &credentialStatusTestClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(pm).
			WithObjects(append([]client.Object{pm}, objects...)...).Build(),
		credentialKey: client.ObjectKey{Name: pm.Name + "-provider-credential", Namespace: pm.Namespace},
	}
	return &PassthroughModelReconciler{Client: c, Scheme: scheme, Config: testConfig()}, c
}

func readCredentialStatusTestModel(t *testing.T, c client.Reader, key client.ObjectKey) *llmv1alpha1.PassthroughModel {
	t.Helper()
	pm := &llmv1alpha1.PassthroughModel{}
	if err := c.Get(context.Background(), key, pm); err != nil {
		t.Fatal(err)
	}
	return pm
}

func bedrockCredentialTestProvider(credential *llmv1alpha1.ProviderCredential) llmv1alpha1.ProviderSpec {
	return llmv1alpha1.ProviderSpec{
		Backend: &llmv1alpha1.ProviderBackend{
			Type: llmv1alpha1.BackendBedrock, Bedrock: &llmv1alpha1.BedrockBackend{Region: "us-west-2"},
		},
		Credential: credential,
	}
}

func TestPassthroughCredentialProviderSelection(t *testing.T) {
	legacy := newPassthroughModel("provider", "default").Spec.Provider
	explicitKey := legacy.DeepCopy()
	explicitKey.Credential = &llmv1alpha1.ProviderCredential{Type: llmv1alpha1.CredentialAPIKey}
	explicitBackend := legacy.DeepCopy()
	explicitBackend.Backend = &llmv1alpha1.ProviderBackend{Type: llmv1alpha1.BackendOpenAI}
	tests := []struct {
		name     string
		provider llmv1alpha1.ProviderSpec
		reads    int
		hostname string
	}{
		{"legacy OpenAI", legacy, 1, "openrouter.ai"},
		{"explicit API key", *explicitKey, 1, "openrouter.ai"},
		{"explicit OpenAI backend", *explicitBackend, 1, "openrouter.ai"},
		{"default workload identity", bedrockCredentialTestProvider(nil), 0, "bedrock-runtime.us-west-2.amazonaws.com"},
		{"explicit workload identity", bedrockCredentialTestProvider(&llmv1alpha1.ProviderCredential{Type: llmv1alpha1.CredentialWorkloadIdentity}), 0, "bedrock-runtime.us-west-2.amazonaws.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := newPassthroughModel("provider", "default")
			pm.Spec.Provider = tt.provider
			secret := namedSecret("provider-provider-credential", pm.Namespace)
			secret.Data = map[string][]byte{"apiKey": []byte("test-key")}
			r, c := newCredentialStatusTestReconciler(t, pm, secret)
			result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pm)})
			if err != nil || result.RequeueAfter != 0 {
				t.Fatalf("reconcile = %+v, %v", result, err)
			}
			if c.credentialReads != tt.reads {
				t.Fatalf("upstream Secret reads = %d, want %d", c.credentialReads, tt.reads)
			}
			stored := readCredentialStatusTestModel(t, c, client.ObjectKeyFromObject(pm))
			if stored.Status.Phase != llmv1alpha1.PassthroughPhaseReady || stored.Status.ProviderHostname != tt.hostname {
				t.Fatalf("provider status = %+v", stored.Status)
			}
			condition := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
			if tt.reads == 0 {
				if condition != nil {
					t.Fatalf("workload identity has a Secret condition: %+v", condition)
				}
			} else if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "Resolved" {
				t.Fatalf("API-key condition = %+v", condition)
			}
		})
	}
}

func TestPassthroughCredentialLookupRetryAndRecovery(t *testing.T) {
	pm := newPassthroughModel("provider", "default")
	secret := namedSecret(pm.Spec.Provider.CredentialSecretName, pm.Namespace)
	secret.Data = map[string][]byte{"apiKey": []byte("test-key")}
	r, c := newCredentialStatusTestReconciler(t, pm, secret)
	c.credentialError = errors.New("temporary cache read failure")
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pm)}
	for range 2 {
		result, err := r.Reconcile(context.Background(), req)
		if err != nil || result.RequeueAfter != time.Minute {
			t.Fatalf("lookup retry = %+v, %v", result, err)
		}
		stored := readCredentialStatusTestModel(t, c, req.NamespacedName)
		condition := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
		if stored.Status.Phase != llmv1alpha1.PassthroughPhaseReady || condition == nil || condition.Status != metav1.ConditionUnknown || condition.Reason != "LookupFailed" {
			t.Fatalf("lookup failure status = %+v", stored.Status)
		}
	}
	c.credentialError = nil
	result, err := r.Reconcile(context.Background(), req)
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("recovery = %+v, %v", result, err)
	}
	stored := readCredentialStatusTestModel(t, c, req.NamespacedName)
	condition := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "Resolved" {
		t.Fatalf("recovered credential = %+v", condition)
	}
}

func TestPassthroughCredentialAbsenceDoesNotBlockReady(t *testing.T) {
	tests := []struct {
		name   string
		data   map[string][]byte
		reason string
	}{
		{"missing Secret", nil, "SecretNotFound"},
		{"missing key", map[string][]byte{"other": []byte("not-an-api-key")}, "APIKeyMissing"},
		{"empty key", map[string][]byte{"apiKey": {}}, "APIKeyMissing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := newPassthroughModel("provider", "default")
			var objects []client.Object
			if tt.data != nil {
				secret := namedSecret(pm.Spec.Provider.CredentialSecretName, pm.Namespace)
				secret.Data = tt.data
				objects = append(objects, secret)
			}
			r, c := newCredentialStatusTestReconciler(t, pm, objects...)
			result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pm)})
			if err != nil || result.RequeueAfter != 0 {
				t.Fatalf("reconcile = %+v, %v", result, err)
			}
			stored := readCredentialStatusTestModel(t, c, client.ObjectKeyFromObject(pm))
			condition := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
			if stored.Status.Phase != llmv1alpha1.PassthroughPhaseReady || condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != tt.reason {
				t.Fatalf("missing credential status = %+v", stored.Status)
			}
		})
	}
}

func TestPassthroughCredentialConditionSurvivesBuildFailure(t *testing.T) {
	pm := newPassthroughModel("provider", "default")
	pm.Spec.Provider.Hostname = ""
	r, c := newCredentialStatusTestReconciler(t, pm)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pm)}); err == nil {
		t.Fatal("expected an invalid-provider build failure")
	}
	stored := readCredentialStatusTestModel(t, c, client.ObjectKeyFromObject(pm))
	condition := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
	backend := meta.FindStatusCondition(stored.Status.Conditions, CondBackendConfigured)
	if condition == nil || condition.Reason != "SecretNotFound" || backend == nil || backend.Reason != "BuildFailed" {
		t.Fatalf("build failure lost credential diagnosis: %+v", stored.Status)
	}
}

func TestPassthroughCredentialTransition(t *testing.T) {
	for _, status := range []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown} {
		for _, invalid := range []bool{false, true} {
			name := string(status) + "/valid"
			if invalid {
				name = string(status) + "/build failure"
			}
			t.Run(name, func(t *testing.T) {
				pm := newPassthroughModel("provider", "default")
				apiKeyProvider := pm.Spec.Provider
				pm.Status.ProviderHostname = apiKeyProvider.Hostname
				pm.Status.Conditions = []metav1.Condition{
					{Type: CondUpstreamCredentialResolved, Status: status, Reason: "PreviousCredential", Message: "previous Secret state"},
					{Type: "OtherObserver", Status: metav1.ConditionTrue, Reason: "Observed", Message: "retain this condition"},
				}
				secret := namedSecret(apiKeyProvider.CredentialSecretName, pm.Namespace)
				secret.Data = map[string][]byte{"apiKey": []byte("test-key")}
				r, c := newCredentialStatusTestReconciler(t, pm, secret)
				req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pm)}
				pm = readCredentialStatusTestModel(t, c, req.NamespacedName)
				pm.Spec.Provider = bedrockCredentialTestProvider(nil)
				if invalid {
					pm.Spec.Provider.Backend.Bedrock.Region = ""
				}
				pm.Generation++
				if err := c.Update(context.Background(), pm); err != nil {
					t.Fatal(err)
				}
				_, err := r.Reconcile(context.Background(), req)
				if (err != nil) != invalid {
					t.Fatalf("transition error = %v, want build failure %t", err, invalid)
				}
				stored := readCredentialStatusTestModel(t, c, req.NamespacedName)
				if condition := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved); condition != nil {
					t.Fatalf("stale Secret condition = %+v", condition)
				}
				if c.credentialReads != 0 || stored.Status.ObservedGeneration != pm.Generation {
					t.Fatalf("transition reads = %d, status = %+v", c.credentialReads, stored.Status)
				}
				if meta.FindStatusCondition(stored.Status.Conditions, "OtherObserver") == nil {
					t.Fatal("unrelated condition was removed")
				}
				backend := meta.FindStatusCondition(stored.Status.Conditions, CondBackendConfigured)
				if invalid && (backend == nil || backend.Reason != "BuildFailed" || stored.Status.Phase != llmv1alpha1.PassthroughPhaseError || stored.Status.ProviderHostname != "") {
					t.Fatalf("build failure status = %+v", stored.Status)
				}
				stored.Spec.Provider = apiKeyProvider
				stored.Generation++
				if err := c.Update(context.Background(), stored); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				stored = readCredentialStatusTestModel(t, c, req.NamespacedName)
				condition := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
				if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != stored.Generation {
					t.Fatalf("restored Secret condition = %+v", condition)
				}
			})
		}
	}
}

func TestPassthroughCredentialLookupPreservesGatewayRetries(t *testing.T) {
	tests := []struct {
		name    string
		failure error
		phase   llmv1alpha1.PassthroughModelPhase
		retry   time.Duration
	}{
		{"missing CRD", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "gateway.envoyproxy.io", Kind: "Backend"}}, llmv1alpha1.PassthroughPhaseError, 15 * time.Second},
		{"apply failure", apierrors.NewForbidden(schema.GroupResource{Group: "gateway.envoyproxy.io", Resource: "backends"}, "provider", errors.New("denied")), llmv1alpha1.PassthroughPhaseError, time.Minute},
		{"cleanup pending", nil, llmv1alpha1.PassthroughPhaseReady, 15 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := newPassthroughModel("provider", "default")
			var objects []client.Object
			if tt.failure == nil {
				disabled := false
				pm.Spec.Endpoints.External.Enabled = &disabled
				objects = append(objects, endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", pm.Namespace, pm.Name+"-external"))
			}
			r, c := newCredentialStatusTestReconciler(t, pm, objects...)
			c.credentialError = errors.New("temporary cache read failure")
			c.backendError = tt.failure
			result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pm)})
			if err != nil || result.RequeueAfter != tt.retry {
				t.Fatalf("gateway retry = %+v, %v; want %s", result, err, tt.retry)
			}
			stored := readCredentialStatusTestModel(t, c, client.ObjectKeyFromObject(pm))
			if stored.Status.Phase != tt.phase {
				t.Fatalf("phase = %s, want %s", stored.Status.Phase, tt.phase)
			}
			credential := meta.FindStatusCondition(stored.Status.Conditions, CondUpstreamCredentialResolved)
			if credential == nil || credential.Status != metav1.ConditionUnknown || credential.Reason != "LookupFailed" {
				t.Fatalf("credential lookup failure was lost: %+v", credential)
			}
			wantReason := "Applied"
			if meta.IsNoMatchError(tt.failure) {
				wantReason = reasonGatewayCRDUnavailable
			} else if tt.failure != nil {
				wantReason = reasonApplyFailed
			}
			backend := meta.FindStatusCondition(stored.Status.Conditions, CondBackendConfigured)
			if backend == nil || backend.Reason != wantReason {
				t.Fatalf("backend condition = %+v, want reason %s", backend, wantReason)
			}
		})
	}
}

func TestIndexWorkloadIdentityCredentialSecret(t *testing.T) {
	for _, credential := range []*llmv1alpha1.ProviderCredential{nil, {Type: llmv1alpha1.CredentialWorkloadIdentity}} {
		pm := newPassthroughModel("provider", "default")
		pm.Spec.Provider = bedrockCredentialTestProvider(credential)
		// Invalid configurations still belong to provider validation, not the Secret watch.
		pm.Spec.Provider.CredentialSecretName = "stale-secret"
		if got := indexPassthroughModelByUpstreamCredentialSecret(pm); len(got) != 0 {
			t.Fatalf("workload identity indexed as %v", got)
		}
	}
}
