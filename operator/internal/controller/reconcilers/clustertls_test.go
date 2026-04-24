/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconcilers

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildSharedCertificate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		baseDomain  string
		namespace   string
		issuer      string
		wantErr     bool
		errContains string
		check       func(t *testing.T, got *unstructured.Unstructured)
	}{
		{
			name:       "happy path: two DNS names, ClusterIssuer ref, secretName matches constant",
			baseDomain: "example.com",
			namespace:  "llm-serving",
			issuer:     "letsencrypt-production",
			check: func(t *testing.T, got *unstructured.Unstructured) {
				if got.GetAPIVersion() != "cert-manager.io/v1" {
					t.Errorf("apiVersion = %q, want cert-manager.io/v1", got.GetAPIVersion())
				}
				if got.GetKind() != "Certificate" {
					t.Errorf("kind = %q, want Certificate", got.GetKind())
				}
				if got.GetName() != SharedTLSCertificateName {
					t.Errorf("name = %q, want %q", got.GetName(), SharedTLSCertificateName)
				}
				if got.GetNamespace() != "llm-serving" {
					t.Errorf("namespace = %q, want llm-serving", got.GetNamespace())
				}
				secretName, _, _ := unstructured.NestedString(got.Object, "spec", "secretName")
				if secretName != SharedTLSSecretName {
					t.Errorf("spec.secretName = %q, want %q", secretName, SharedTLSSecretName)
				}
				dnsNames, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "dnsNames")
				wantDNS := []string{"llm.example.com", "llm-internal.example.com"}
				if len(dnsNames) != len(wantDNS) {
					t.Fatalf("dnsNames = %v, want %v", dnsNames, wantDNS)
				}
				for i := range wantDNS {
					if dnsNames[i] != wantDNS[i] {
						t.Errorf("dnsNames[%d] = %q, want %q", i, dnsNames[i], wantDNS[i])
					}
				}
				issuerKind, _, _ := unstructured.NestedString(got.Object, "spec", "issuerRef", "kind")
				issuerName, _, _ := unstructured.NestedString(got.Object, "spec", "issuerRef", "name")
				issuerGroup, _, _ := unstructured.NestedString(got.Object, "spec", "issuerRef", "group")
				if issuerKind != "ClusterIssuer" {
					t.Errorf("issuerRef.kind = %q, want ClusterIssuer", issuerKind)
				}
				if issuerName != "letsencrypt-production" {
					t.Errorf("issuerRef.name = %q, want letsencrypt-production", issuerName)
				}
				if issuerGroup != "cert-manager.io" {
					t.Errorf("issuerRef.group = %q, want cert-manager.io", issuerGroup)
				}
			},
		},
		{
			name:        "rejects empty baseDomain",
			baseDomain:  "",
			namespace:   "llm-serving",
			issuer:      "letsencrypt-production",
			wantErr:     true,
			errContains: "baseDomain",
		},
		{
			name:        "rejects empty namespace",
			baseDomain:  "example.com",
			namespace:   "",
			issuer:      "letsencrypt-production",
			wantErr:     true,
			errContains: "namespace",
		},
		{
			name:        "rejects empty clusterIssuerName",
			baseDomain:  "example.com",
			namespace:   "llm-serving",
			issuer:      "",
			wantErr:     true,
			errContains: "clusterIssuerName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := BuildSharedCertificate(tt.baseDomain, tt.namespace, tt.issuer)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestBuildSecretReferenceGrant(t *testing.T) {
	t.Parallel()

	const fromNS = "envoy-gateway-system"
	got := BuildSecretReferenceGrant(fromNS, "llm-serving")
	if got.GetAPIVersion() != "gateway.networking.k8s.io/v1beta1" {
		t.Errorf("apiVersion = %q, want gateway.networking.k8s.io/v1beta1", got.GetAPIVersion())
	}
	if got.GetKind() != "ReferenceGrant" {
		t.Errorf("kind = %q, want ReferenceGrant", got.GetKind())
	}
	if got.GetNamespace() != "llm-serving" {
		t.Errorf("namespace = %q, want llm-serving (Secret's namespace)", got.GetNamespace())
	}
	if !strings.Contains(got.GetName(), fromNS) {
		t.Errorf("name %q should include source namespace for uniqueness", got.GetName())
	}
	from, _, _ := unstructured.NestedSlice(got.Object, "spec", "from")
	if len(from) != 1 {
		t.Fatalf("expected 1 from entry, got %d", len(from))
	}
	fromEntry, _ := from[0].(map[string]interface{})
	if fromEntry["kind"] != "Gateway" {
		t.Errorf("from.kind = %v, want Gateway", fromEntry["kind"])
	}
	if fromEntry["namespace"] != fromNS {
		t.Errorf("from.namespace = %v, want %q", fromEntry["namespace"], fromNS)
	}
	to, _, _ := unstructured.NestedSlice(got.Object, "spec", "to")
	if len(to) != 1 {
		t.Fatalf("expected 1 to entry, got %d", len(to))
	}
	toEntry, _ := to[0].(map[string]interface{})
	if toEntry["kind"] != "Secret" {
		t.Errorf("to.kind = %v, want Secret", toEntry["kind"])
	}
	if toEntry["name"] != SharedTLSSecretName {
		t.Errorf("to.name = %v, want %q", toEntry["name"], SharedTLSSecretName)
	}
}

// fakeGateway returns a minimal Gateway unstructured with the given listeners.
func fakeGateway(t *testing.T, listeners []interface{}) *unstructured.Unstructured {
	t.Helper()
	gw := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]interface{}{
				"name":      "nebari-gateway",
				"namespace": "envoy-gateway-system",
			},
			"spec": map[string]interface{}{
				"gatewayClassName": "envoy",
				"listeners":        listeners,
			},
		},
	}
	return gw
}

// listenerByName returns the listener map with the given name from the Gateway,
// or nil if absent.
func listenerByName(t *testing.T, gw *unstructured.Unstructured, name string) map[string]interface{} {
	t.Helper()
	ls, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	for _, raw := range ls {
		l, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if l["name"] == name {
			return l
		}
	}
	return nil
}

func TestMergeHTTPSListener(t *testing.T) {
	t.Parallel()

	const (
		listenerName    = "llm-https"
		hostname        = "llm.example.com"
		secretName      = "nebari-llm-shared-tls"
		secretNamespace = "llm-serving"
	)

	tests := []struct {
		name        string
		listeners   []interface{}
		wantChanged bool
		check       func(t *testing.T, gw *unstructured.Unstructured)
	}{
		{
			name:        "empty listeners: appends the HTTPS listener",
			listeners:   []interface{}{},
			wantChanged: true,
			check: func(t *testing.T, gw *unstructured.Unstructured) {
				ls, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
				if len(ls) != 1 {
					t.Fatalf("expected 1 listener, got %d", len(ls))
				}
				l := listenerByName(t, gw, listenerName)
				if l == nil {
					t.Fatal("expected to find listener by name")
				}
				if l["protocol"] != "HTTPS" {
					t.Errorf("protocol = %v, want HTTPS", l["protocol"])
				}
				if l["port"] != int64(443) {
					t.Errorf("port = %v, want 443", l["port"])
				}
				if l["hostname"] != hostname {
					t.Errorf("hostname = %v, want %q", l["hostname"], hostname)
				}
			},
		},
		{
			name: "preserves unrelated listeners (argocd, keycloak, bare domain)",
			listeners: []interface{}{
				map[string]interface{}{
					"name":     "https-argocd",
					"protocol": "HTTPS",
					"port":     int64(443),
					"hostname": "argocd.example.com",
				},
				map[string]interface{}{
					"name":     "https-keycloak",
					"protocol": "HTTPS",
					"port":     int64(443),
					"hostname": "keycloak.example.com",
				},
			},
			wantChanged: true,
			check: func(t *testing.T, gw *unstructured.Unstructured) {
				ls, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
				if len(ls) != 3 {
					t.Fatalf("expected 3 listeners (2 existing + 1 added), got %d", len(ls))
				}
				for _, name := range []string{"https-argocd", "https-keycloak", listenerName} {
					if listenerByName(t, gw, name) == nil {
						t.Errorf("expected listener %q to be present", name)
					}
				}
			},
		},
		{
			name: "updates existing listener of same name when fields differ",
			listeners: []interface{}{
				map[string]interface{}{
					"name":     listenerName,
					"protocol": "HTTP",
					"port":     int64(80),
					"hostname": "old.example.com",
				},
			},
			wantChanged: true,
			check: func(t *testing.T, gw *unstructured.Unstructured) {
				l := listenerByName(t, gw, listenerName)
				if l == nil {
					t.Fatal("expected listener to be present")
				}
				if l["protocol"] != "HTTPS" {
					t.Errorf("protocol = %v, want HTTPS (should have been updated)", l["protocol"])
				}
				if l["hostname"] != hostname {
					t.Errorf("hostname = %v, want %q (should have been updated)", l["hostname"], hostname)
				}
			},
		},
		{
			name: "idempotent: no change when listener already matches desired state",
			listeners: []interface{}{
				buildHTTPSListener(listenerName, hostname, secretName, secretNamespace),
			},
			wantChanged: false,
			check: func(t *testing.T, gw *unstructured.Unstructured) {
				ls, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
				if len(ls) != 1 {
					t.Fatalf("expected 1 listener, got %d", len(ls))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gw := fakeGateway(t, tt.listeners)
			changed, err := MergeHTTPSListener(gw, listenerName, hostname, secretName, secretNamespace)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if tt.check != nil {
				tt.check(t, gw)
			}
		})
	}
}

func TestMergeHTTPSListener_NilGateway(t *testing.T) {
	t.Parallel()
	_, err := MergeHTTPSListener(nil, "x", "y", "z", "w")
	if err == nil {
		t.Fatal("expected error for nil gateway")
	}
}

func TestMergeHTTPSListener_DoubleApplyIsIdempotent(t *testing.T) {
	// A reconciler safety guarantee: calling the function twice in a row with
	// the same inputs must result in no-change on the second call.
	t.Parallel()
	gw := fakeGateway(t, []interface{}{})
	changed1, err := MergeHTTPSListener(gw, "llm-https", "llm.example.com", "shared-tls", "llm-serving")
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if !changed1 {
		t.Fatal("first call: expected changed=true on empty Gateway")
	}
	changed2, err := MergeHTTPSListener(gw, "llm-https", "llm.example.com", "shared-tls", "llm-serving")
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if changed2 {
		t.Error("second call: expected changed=false (idempotent)")
	}
}
