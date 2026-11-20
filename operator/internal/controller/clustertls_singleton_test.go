/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// newFakeClientWithGateway builds a fake client that knows about the
// gateway.networking.k8s.io/v1 Gateway kind and seeds it with the external
// and internal Gateways the operator expects to patch.
func newFakeClientWithGateway(t *testing.T, extraObjs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	// Register the kinds we touch as unstructured via a GVK-aware scheme.
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GatewayList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "CertificateList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrant"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrantList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "", Version: "v1", Kind: "SecretList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(clientTrafficPolicyGVK(), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "ClientTrafficPolicyList"},
		&unstructured.UnstructuredList{},
	)

	extGW := newGateway(t, "nebari-gateway", "envoy-gateway-system", []interface{}{
		// Pre-existing unrelated listener - the operator must preserve it.
		map[string]interface{}{
			"name":     "https-argocd",
			"protocol": "HTTPS",
			"port":     int64(443),
			"hostname": "argocd.example.com",
		},
	})
	intGW := newGateway(t, "nebari-internal-gateway", "envoy-gateway-system", []interface{}{})

	objs := make([]client.Object, 0, 2+len(extraObjs))
	objs = append(objs, extGW, intGW)
	objs = append(objs, extraObjs...)

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

func newGateway(t *testing.T, name, namespace string, listeners []interface{}) *unstructured.Unstructured {
	t.Helper()
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"})
	gw.SetName(name)
	gw.SetNamespace(namespace)
	_ = unstructured.SetNestedField(gw.Object, "envoy", "spec", "gatewayClassName")
	_ = unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners")
	return gw
}

// newTestSingleton returns the shared-listener reconciler with an Event
// recorder that keeps events in memory.
func newTestSingleton(c client.Client, cfg *config.OperatorConfig) *ClusterTLSSingleton {
	return &ClusterTLSSingleton{Client: c, Config: cfg, Recorder: record.NewFakeRecorder(100)}
}

func testOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:            "example.com",
		ExternalGatewayName:   "nebari-gateway",
		ExternalGatewayNS:     "envoy-gateway-system",
		InternalGatewayName:   "nebari-internal-gateway",
		InternalGatewayNS:     "envoy-gateway-system",
		OperatorNamespace:     "llm-serving",
		ClusterIssuerName:     "letsencrypt-production",
		ManageSharedListeners: true,
	}
}

func getUnstructured(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name, namespace string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, obj); err != nil {
		return nil
	}
	return obj
}

func TestClusterTLSSingleton_Reconcile(t *testing.T) {
	tests := []struct {
		name      string
		mutateCfg func(cfg *config.OperatorConfig)
		check     func(t *testing.T, c client.Client, cfg *config.OperatorConfig)
	}{
		{
			name: "happy path: creates Certificate, ReferenceGrant, and patches both Gateways",
			check: func(t *testing.T, c client.Client, cfg *config.OperatorConfig) {
				// Certificate created
				cert := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
					reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace)
				if cert == nil {
					t.Fatal("expected Certificate to be created")
				}
				dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
				if len(dnsNames) != 2 || dnsNames[0] != "llm.example.com" || dnsNames[1] != "llm-internal.example.com" {
					t.Errorf("unexpected dnsNames: %v", dnsNames)
				}

				// ReferenceGrant created in operator ns for external gateway ns
				rg := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrant"},
					"nebari-llm-shared-tls-from-envoy-gateway-system", cfg.OperatorNamespace)
				if rg == nil {
					t.Fatal("expected ReferenceGrant to be created in operator namespace")
				}

				// External Gateway has both the existing listener AND the new HTTPS listener
				gw := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
					"nebari-gateway", "envoy-gateway-system")
				if gw == nil {
					t.Fatal("expected external Gateway")
				}
				listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
				if len(listeners) != 2 {
					t.Fatalf("expected 2 external listeners (pre-existing + llm), got %d", len(listeners))
				}
				if !hasListenerNamed(listeners, "https-argocd") {
					t.Error("expected pre-existing argocd listener to be preserved")
				}
				if !hasListenerNamed(listeners, reconcilers.ExternalHTTPSListenerName) {
					t.Error("expected llm-https listener to be added")
				}

				// Internal Gateway has the new listener
				intGW := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
					"nebari-internal-gateway", "envoy-gateway-system")
				if intGW == nil {
					t.Fatal("expected internal Gateway")
				}
				intL, _, _ := unstructured.NestedSlice(intGW.Object, "spec", "listeners")
				if !hasListenerNamed(intL, reconcilers.InternalHTTPSListenerName) {
					t.Error("expected llm-internal-https listener to be added")
				}
			},
		},
		{
			name: "manageSharedListeners=false: still creates Certificate, skips Gateway patch",
			mutateCfg: func(cfg *config.OperatorConfig) {
				cfg.ManageSharedListeners = false
			},
			check: func(t *testing.T, c client.Client, cfg *config.OperatorConfig) {
				cert := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
					reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace)
				if cert == nil {
					t.Fatal("Certificate should still be created when manageSharedListeners=false")
				}
				gw := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
					"nebari-gateway", "envoy-gateway-system")
				listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
				if hasListenerNamed(listeners, reconcilers.ExternalHTTPSListenerName) {
					t.Error("should NOT have added llm-https listener when manageSharedListeners=false")
				}
			},
		},
		{
			name: "empty baseDomain: no-op, returns no error",
			mutateCfg: func(cfg *config.OperatorConfig) {
				cfg.BaseDomain = ""
			},
			check: func(t *testing.T, c client.Client, cfg *config.OperatorConfig) {
				cert := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
					reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace)
				if cert != nil {
					t.Error("no Certificate should be created when baseDomain is empty")
				}
			},
		},
		{
			name: "empty OperatorNamespace (test mode): no-op, returns no error",
			mutateCfg: func(cfg *config.OperatorConfig) {
				cfg.OperatorNamespace = ""
			},
			check: func(t *testing.T, c client.Client, cfg *config.OperatorConfig) {
				// Nothing meaningful to check in the cluster; the important
				// assertion is that Reconcile returned nil (verified by the
				// t.Fatalf guard in the test driver below). Exercise: confirm
				// no Certificate was accidentally created in the empty namespace.
				cert := getUnstructured(t, c,
					schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
					reconcilers.SharedTLSCertificateName, "")
				if cert != nil {
					t.Error("no Certificate should be created with empty OperatorNamespace")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testOperatorConfig()
			if tt.mutateCfg != nil {
				tt.mutateCfg(cfg)
			}
			c := newFakeClientWithGateway(t)
			r := newTestSingleton(c, cfg)
			if err := r.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			tt.check(t, c, cfg)
		})
	}
}

func TestClusterTLSSingleton_Reconcile_Idempotent(t *testing.T) {
	cfg := testOperatorConfig()
	c := newFakeClientWithGateway(t)
	r := newTestSingleton(c, cfg)

	for i := 0; i < 3; i++ {
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile pass %d: %v", i, err)
		}
	}

	// After 3 passes, there should still be exactly one Certificate and the
	// external Gateway should still have exactly 2 listeners.
	cert := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace)
	if cert == nil {
		t.Fatal("expected Certificate to exist after idempotent reconciles")
	}

	gw := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
		"nebari-gateway", "envoy-gateway-system")
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if len(listeners) != 2 {
		t.Errorf("expected 2 listeners after idempotent reconciles, got %d", len(listeners))
	}
}

func TestClusterTLSSingleton_Reconcile_UpdatesDriftedCertificate(t *testing.T) {
	cfg := testOperatorConfig()
	// Pre-seed a Certificate with drifted dnsNames; reconcile should fix it.
	driftedCert := &unstructured.Unstructured{}
	driftedCert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	driftedCert.SetName(reconcilers.SharedTLSCertificateName)
	driftedCert.SetNamespace(cfg.OperatorNamespace)
	_ = unstructured.SetNestedField(driftedCert.Object, "wrong-secret", "spec", "secretName")
	_ = unstructured.SetNestedStringSlice(driftedCert.Object, []string{"old.example.com"}, "spec", "dnsNames")

	c := newFakeClientWithGateway(t, driftedCert)
	r := newTestSingleton(c, cfg)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	cert := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace)
	secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if secretName != reconcilers.SharedTLSSecretName {
		t.Errorf("spec.secretName = %q, want %q (drift should have been corrected)", secretName, reconcilers.SharedTLSSecretName)
	}
	dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	if len(dnsNames) != 2 {
		t.Errorf("dnsNames = %v, want 2 entries", dnsNames)
	}
}

func TestClusterTLSSingleton_Reconcile_MissingGateway_IsNotAnError(t *testing.T) {
	cfg := testOperatorConfig()
	// Seed only the external Gateway; internal is absent.
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GatewayList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "CertificateList"},
		&unstructured.UnstructuredList{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrant"},
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrantList"},
		&unstructured.UnstructuredList{},
	)

	extGW := newGateway(t, "nebari-gateway", "envoy-gateway-system", []interface{}{})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(extGW).Build()
	r := newTestSingleton(c, cfg)
	// Should not error just because internal Gateway is missing.
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile should tolerate missing Gateway, got error: %v", err)
	}
	// Certificate should still be created.
	cert := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace)
	if cert == nil {
		t.Error("Certificate should still be created when a Gateway is missing")
	}
	// External Gateway should have been patched.
	gw := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
		"nebari-gateway", "envoy-gateway-system")
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if !hasListenerNamed(listeners, reconcilers.ExternalHTTPSListenerName) {
		t.Error("external Gateway should have been patched even though internal was missing")
	}
}

func hasListenerNamed(listeners []interface{}, name string) bool {
	for _, raw := range listeners {
		l, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if l["name"] == name {
			return true
		}
	}
	return false
}

// Verify apierrors import is actually used (future-proofing for tests that
// call into NotFound handling directly).
var _ = apierrors.IsNotFound

// userProvidedTLSSecret is the bring-your-own-certificate Secret name used
// across the user-provided-secret tests.
const userProvidedTLSSecret = "nebari-wildcard-tls"

func TestClusterTLSSingleton_Reconcile_UserProvidedSecret(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.TLSSecretName = userProvidedTLSSecret

	// Seed a previously managed Certificate (from cert-manager mode) and the
	// user-provided Secret to simulate an in-place switchover.
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	cert.SetName(reconcilers.SharedTLSCertificateName)
	cert.SetNamespace(cfg.OperatorNamespace)

	secret := &unstructured.Unstructured{}
	secret.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"})
	secret.SetName(userProvidedTLSSecret)
	secret.SetNamespace(cfg.OperatorNamespace)
	_ = unstructured.SetNestedField(secret.Object, "kubernetes.io/tls", "type")

	c := newFakeClientWithGateway(t, cert, secret)
	r := newTestSingleton(c, cfg)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// The previously managed Certificate must be deleted, not left for
	// cert-manager to keep renewing.
	if got := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace); got != nil {
		t.Error("managed Certificate should be deleted when TLSSecretName is set")
	}

	// The ReferenceGrant must target the user-provided Secret.
	rg := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrant"},
		"nebari-llm-shared-tls-from-envoy-gateway-system", cfg.OperatorNamespace)
	if rg == nil {
		t.Fatal("expected ReferenceGrant to be created")
	}
	to, _, _ := unstructured.NestedSlice(rg.Object, "spec", "to")
	if len(to) != 1 {
		t.Fatalf("expected 1 to entry, got %d", len(to))
	}
	toEntry, _ := to[0].(map[string]interface{})
	if toEntry["name"] != userProvidedTLSSecret {
		t.Errorf("ReferenceGrant to.name = %v, want %q", toEntry["name"], userProvidedTLSSecret)
	}

	// Both shared listeners must terminate TLS with the user-provided Secret.
	for _, tc := range []struct{ gateway, listener string }{
		{"nebari-gateway", reconcilers.ExternalHTTPSListenerName},
		{"nebari-internal-gateway", reconcilers.InternalHTTPSListenerName},
	} {
		gw := getUnstructured(t, c,
			schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
			tc.gateway, "envoy-gateway-system")
		if gw == nil {
			t.Fatalf("expected Gateway %s", tc.gateway)
		}
		listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
		found := false
		for _, raw := range listeners {
			l, _ := raw.(map[string]interface{})
			if l["name"] != tc.listener {
				continue
			}
			found = true
			tls, _ := l["tls"].(map[string]interface{})
			refs, _ := tls["certificateRefs"].([]interface{})
			if len(refs) != 1 {
				t.Fatalf("%s: expected 1 certificateRef, got %d", tc.listener, len(refs))
			}
			ref, _ := refs[0].(map[string]interface{})
			if ref["name"] != userProvidedTLSSecret {
				t.Errorf("%s certificateRef name = %v, want %q", tc.listener, ref["name"], userProvidedTLSSecret)
			}
		}
		if !found {
			t.Errorf("expected listener %s on Gateway %s", tc.listener, tc.gateway)
		}
	}
}

func TestClusterTLSSingleton_Reconcile_UserProvidedSecret_MissingSecretIsNotAnError(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.TLSSecretName = userProvidedTLSSecret

	c := newFakeClientWithGateway(t)
	r := newTestSingleton(c, cfg)
	// The secret does not exist yet; the listeners must still be attached
	// (Envoy serves as soon as the user creates it) and Reconcile must not
	// error - mirroring nebari-operator's UserProvidedSecretNotFound
	// behaviour of reporting without blocking.
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile should tolerate a missing user secret, got: %v", err)
	}

	if got := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
		reconcilers.SharedTLSCertificateName, cfg.OperatorNamespace); got != nil {
		t.Error("no Certificate should be created when TLSSecretName is set")
	}

	gw := getUnstructured(t, c,
		schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
		"nebari-gateway", "envoy-gateway-system")
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if !hasListenerNamed(listeners, reconcilers.ExternalHTTPSListenerName) {
		t.Error("llm-https listener should be attached even while the user secret is missing")
	}
}

// TestClusterTLSSingleton_PatchListenerConflict covers a shared Gateway that
// another writer updates between the operator's read and write. External and
// internal Gateways are the same object, as on NIC, so one reconcile makes
// two Updates to it: the first adds llm-https, the second llm-internal-https.
func TestClusterTLSSingleton_PatchListenerConflict(t *testing.T) {
	gatewayGR := schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "gateways"}
	tests := []struct {
		name string
		// failUpdate returns the error to inject for the n-th Gateway
		// Update (1-based), or nil to let it through.
		failUpdate      func(n int) error
		wantErr         func(error) bool
		wantBoth        bool
		wantUpdateCalls int    // 0 means not checked
		wantErrContains string // empty means not checked
	}{
		{
			name: "one conflict on the internal patch",
			failUpdate: func(n int) error {
				if n == 2 {
					return apierrors.NewConflict(gatewayGR, "nebari-gateway", nil)
				}
				return nil
			},
			wantBoth: true,
		},
		{
			name: "one conflict on the external patch",
			failUpdate: func(n int) error {
				if n == 1 {
					return apierrors.NewConflict(gatewayGR, "nebari-gateway", nil)
				}
				return nil
			},
			wantBoth: true,
		},
		{
			name: "conflicts beyond the retry budget",
			failUpdate: func(int) error {
				return apierrors.NewConflict(gatewayGR, "nebari-gateway", nil)
			},
			wantErr:         apierrors.IsConflict,
			wantUpdateCalls: retry.DefaultBackoff.Steps,
			wantErrContains: "envoy-gateway-system/nebari-gateway",
		},
		{
			name: "non-conflict error is not retried",
			failUpdate: func(n int) error {
				if n == 1 {
					return apierrors.NewForbidden(gatewayGR, "nebari-gateway", nil)
				}
				return nil
			},
			wantErr:         apierrors.IsForbidden,
			wantUpdateCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testOperatorConfig()
			cfg.InternalGatewayName = cfg.ExternalGatewayName
			cfg.InternalGatewayNS = cfg.ExternalGatewayNS

			gatewayUpdates := 0
			base := newFakeClientWithGateway(t)
			c := interceptor.NewClient(base, interceptor.Funcs{
				Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if obj.GetObjectKind().GroupVersionKind() != gatewayGVK() {
						return cl.Update(ctx, obj, opts...)
					}
					gatewayUpdates++
					injected := tt.failUpdate(gatewayUpdates)
					if injected == nil {
						return cl.Update(ctx, obj, opts...)
					}
					if apierrors.IsConflict(injected) {
						// Act as the other writer: bump the stored Gateway so
						// a retry that reuses the stale object would be
						// rejected by the fake client too.
						other := &unstructured.Unstructured{}
						other.SetGroupVersionKind(gatewayGVK())
						if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), other); err != nil {
							t.Fatalf("other writer get: %v", err)
						}
						other.SetAnnotations(map[string]string{"other-writer": fmt.Sprint(gatewayUpdates)})
						if err := cl.Update(ctx, other); err != nil {
							t.Fatalf("other writer update: %v", err)
						}
					}
					return injected
				},
			})
			r := newTestSingleton(c, cfg)

			err := r.Reconcile(context.Background())
			if tt.wantErr == nil && err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if tt.wantErr != nil && (err == nil || !tt.wantErr(err)) {
				t.Fatalf("Reconcile error = %v, want a matching error", err)
			}
			if tt.wantErrContains != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErrContains)) {
				t.Errorf("Reconcile error = %v, want it to contain %q", err, tt.wantErrContains)
			}
			if tt.wantUpdateCalls != 0 && gatewayUpdates != tt.wantUpdateCalls {
				t.Errorf("Gateway Update calls = %d, want %d", gatewayUpdates, tt.wantUpdateCalls)
			}
			if tt.wantBoth {
				gw := getUnstructured(t, base, gatewayGVK(), cfg.ExternalGatewayName, cfg.ExternalGatewayNS)
				if gw == nil {
					t.Fatal("expected shared Gateway")
				}
				listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
				for _, name := range []string{reconcilers.ExternalHTTPSListenerName, reconcilers.InternalHTTPSListenerName} {
					if !hasListenerNamed(listeners, name) {
						t.Errorf("expected listener %s on the shared Gateway", name)
					}
				}
			}
		})
	}
}

// TestClusterTLSSingleton_RetryAfterFailure drives Start and checks that a
// failed reconcile is retried after RetryInterval, doubling on each further
// failure, while a successful one waits the full Interval.
func TestClusterTLSSingleton_RetryAfterFailure(t *testing.T) {
	const (
		defaultRetry = 20 * time.Millisecond
		waitTimeout  = 2 * time.Second
	)
	gatewayGR := schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "gateways"}
	certGVK := schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	certGR := schema.GroupResource{Group: "cert-manager.io", Resource: "certificates"}

	tests := []struct {
		name string
		// failingUpdates is how many Gateway Updates return Forbidden, counted
		// from the first. A Forbidden external patch ends the reconcile, so
		// each failing Update is one failed reconcile.
		failingUpdates int
		// retryInterval overrides defaultRetry. The gap-growth row uses a
		// longer one so scheduler noise on a loaded machine (seen at 10 to
		// 13 ms) cannot erase the 1.5x margin between the first two gaps.
		retryInterval time.Duration
		wantBoth      bool
		wantGapGrowth bool
		// observe, when set, is how long to watch after the first reconcile
		// before checking the update and reconcile counts.
		observe        time.Duration
		wantUpdates    int
		wantReconciles int

		// interval overrides the default Interval of one hour.
		interval time.Duration
		// failReconcile, when set, fails the n-th reconcile (1-based) by
		// returning Forbidden from its Certificate Get. Unlike a failing
		// Gateway Update, this also fails a reconcile that has nothing left
		// to patch.
		failReconcile func(n int) bool
		// waitReconciles is how many reconciles to wait for before calling
		// checkGaps with the gaps between consecutive reconciles.
		waitReconciles int
		checkGaps      func(t *testing.T, gaps []time.Duration)
		// spinWindow, when set, is how long to let Start run from the
		// beginning before checking it made at most maxReconciles passes.
		spinWindow    time.Duration
		maxReconciles int
	}{
		{
			name:           "one failed reconcile is retried quickly",
			failingUpdates: 1,
			wantBoth:       true,
		},
		{
			name:           "repeated failures back off",
			failingUpdates: 3,
			retryInterval:  100 * time.Millisecond,
			wantBoth:       true,
			wantGapGrowth:  true,
		},
		{
			name:           "success waits the full interval",
			failingUpdates: 0,
			observe:        300 * time.Millisecond,
			wantUpdates:    2,
			wantReconciles: 1,
		},
		{
			// Reconciles 1, 2 fail (waits 100, 200 ms), 3 succeeds (waits
			// Interval), 4 fails. With the reset, the wait after 4 is 100 ms
			// again; without it, it would be 400 ms.
			name:          "a success resets the retry delay",
			interval:      500 * time.Millisecond,
			retryInterval: 100 * time.Millisecond,
			failReconcile: func(n int) bool {
				return n == 1 || n == 2 || n == 4
			},
			waitReconciles: 5,
			checkGaps: func(t *testing.T, gaps []time.Duration) {
				if limit := 150 * time.Millisecond; gaps[3] >= limit {
					t.Errorf("retry after the failure that followed a success took %s, want under %s (retry delay not reset)", gaps[3], limit)
				}
			},
		},
		{
			// Waits would be 100, 200, 400 ms uncapped; capped they are
			// 100, 150, 150 ms.
			name:           "the retry delay is capped at the interval",
			interval:       150 * time.Millisecond,
			retryInterval:  100 * time.Millisecond,
			failReconcile:  func(int) bool { return true },
			waitReconciles: 4,
			checkGaps: func(t *testing.T, gaps []time.Duration) {
				limit := 150*time.Millisecond + 75*time.Millisecond // interval plus a scheduling margin
				for i, g := range gaps {
					if g > limit {
						t.Errorf("gap %d = %s, want at most %s (retry delay not capped at the interval)", i, g, limit)
					}
				}
			},
		},
		{
			name:          "a negative retry interval uses the default",
			retryInterval: -1,
			failReconcile: func(int) bool { return true },
			spinWindow:    200 * time.Millisecond,
			maxReconciles: 2,
		},
		{
			name:          "a negative interval uses the default",
			interval:      -1,
			spinWindow:    200 * time.Millisecond,
			maxReconciles: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testOperatorConfig()

			var (
				mu          sync.Mutex
				updateTimes []time.Time // every Gateway Update, failed or not
				certGets    int         // one per reconcile pass
				certTimes   []time.Time // when each reconcile pass started
			)
			base := newFakeClientWithGateway(t)
			c := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if obj.GetObjectKind().GroupVersionKind() == certGVK {
						mu.Lock()
						certGets++
						certTimes = append(certTimes, time.Now())
						n := certGets
						mu.Unlock()
						if tt.failReconcile != nil && tt.failReconcile(n) {
							return apierrors.NewForbidden(certGR, key.Name, nil)
						}
					}
					return cl.Get(ctx, key, obj, opts...)
				},
				Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if obj.GetObjectKind().GroupVersionKind() != gatewayGVK() {
						return cl.Update(ctx, obj, opts...)
					}
					mu.Lock()
					updateTimes = append(updateTimes, time.Now())
					n := len(updateTimes)
					mu.Unlock()
					if n <= tt.failingUpdates {
						return apierrors.NewForbidden(gatewayGR, obj.GetName(), nil)
					}
					return cl.Update(ctx, obj, opts...)
				},
			})
			r := newTestSingleton(c, cfg)
			r.Interval = time.Hour
			if tt.interval != 0 {
				r.Interval = tt.interval
			}
			r.RetryInterval = defaultRetry
			if tt.retryInterval != 0 {
				r.RetryInterval = tt.retryInterval
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- r.Start(ctx) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("Start returned error: %v", err)
					}
				case <-time.After(waitTimeout):
					t.Error("Start did not return after ctx was cancelled")
				}
			}()

			bothListeners := func() bool { return sharedListenersPresent(t, base, cfg) }

			if tt.wantBoth {
				if !waitFor(waitTimeout, bothListeners) {
					t.Fatalf("both listeners not present within %s", waitTimeout)
				}
			}

			if tt.wantGapGrowth {
				mu.Lock()
				times := append([]time.Time(nil), updateTimes...)
				mu.Unlock()
				if len(times) < tt.failingUpdates+1 {
					t.Fatalf("saw %d Gateway updates, want at least %d", len(times), tt.failingUpdates+1)
				}
				// Gaps between the failing reconciles' updates.
				first := times[1].Sub(times[0])
				second := times[2].Sub(times[1])
				if float64(second) < 1.5*float64(first) {
					t.Errorf("second gap %s is less than 1.5x the first gap %s", second, first)
				}
			}

			if tt.observe > 0 {
				if !waitFor(waitTimeout, bothListeners) {
					t.Fatalf("first reconcile did not finish within %s", waitTimeout)
				}
				time.Sleep(tt.observe)
				mu.Lock()
				gotUpdates, gotReconciles := len(updateTimes), certGets
				mu.Unlock()
				if gotUpdates != tt.wantUpdates {
					t.Errorf("Gateway updates during %s = %d, want %d", tt.observe, gotUpdates, tt.wantUpdates)
				}
				if gotReconciles != tt.wantReconciles {
					t.Errorf("reconciles during %s = %d, want %d", tt.observe, gotReconciles, tt.wantReconciles)
				}
			}

			if tt.checkGaps != nil {
				tt.checkGaps(t, waitForGaps(t, &mu, &certTimes, tt.waitReconciles))
			}

			if tt.spinWindow > 0 {
				time.Sleep(tt.spinWindow)
				mu.Lock()
				got := certGets
				mu.Unlock()
				if got > tt.maxReconciles {
					t.Errorf("reconciles during the first %s = %d, want at most %d", tt.spinWindow, got, tt.maxReconciles)
				}
			}
		})
	}
}

// sharedListenersPresent reports whether the external Gateway has llm-https
// and the internal Gateway has llm-internal-https.
func sharedListenersPresent(t *testing.T, c client.Client, cfg *config.OperatorConfig) bool {
	t.Helper()
	ext := getUnstructured(t, c, gatewayGVK(), cfg.ExternalGatewayName, cfg.ExternalGatewayNS)
	in := getUnstructured(t, c, gatewayGVK(), cfg.InternalGatewayName, cfg.InternalGatewayNS)
	if ext == nil || in == nil {
		return false
	}
	extL, _, _ := unstructured.NestedSlice(ext.Object, "spec", "listeners")
	inL, _, _ := unstructured.NestedSlice(in.Object, "spec", "listeners")
	return hasListenerNamed(extL, reconcilers.ExternalHTTPSListenerName) &&
		hasListenerNamed(inL, reconcilers.InternalHTTPSListenerName)
}

// waitForGaps waits up to 5 s until times (guarded by mu) holds at least n
// entries, then returns the n-1 gaps between the first n.
func waitForGaps(t *testing.T, mu *sync.Mutex, times *[]time.Time, n int) []time.Duration {
	t.Helper()
	enough := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(*times) >= n
	}
	if !waitFor(5*time.Second, enough) {
		t.Fatalf("fewer than %d reconciles within 5s", n)
	}
	mu.Lock()
	got := append([]time.Time(nil), (*times)[:n]...)
	mu.Unlock()
	gaps := make([]time.Duration, 0, n-1)
	for i := 1; i < len(got); i++ {
		gaps = append(gaps, got[i].Sub(got[i-1]))
	}
	return gaps
}

// waitFor polls cond until it returns true or timeout passes.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}
