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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
			r := &ClusterTLSSingleton{Client: c, Config: cfg}
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
	r := &ClusterTLSSingleton{Client: c, Config: cfg}

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
	r := &ClusterTLSSingleton{Client: c, Config: cfg}
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
	r := &ClusterTLSSingleton{Client: c, Config: cfg}
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
	r := &ClusterTLSSingleton{Client: c, Config: cfg}
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
	r := &ClusterTLSSingleton{Client: c, Config: cfg}
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
