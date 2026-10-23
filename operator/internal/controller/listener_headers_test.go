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
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// headerPolicy returns the ClientTrafficPolicy the operator manages for a
// shared listener, or nil.
func headerPolicy(t *testing.T, c client.Client, listener string) *unstructured.Unstructured {
	t.Helper()
	return getUnstructured(t, c, clientTrafficPolicyGVK(), reconcilers.ListenerHeaderPolicyName(listener), "envoy-gateway-system")
}

// platformPolicy is a ClientTrafficPolicy the cluster admin owns.
func platformPolicy(name string, spec map[string]interface{}) *unstructured.Unstructured {
	p := &unstructured.Unstructured{}
	p.SetGroupVersionKind(clientTrafficPolicyGVK())
	p.SetName(name)
	p.SetNamespace("envoy-gateway-system")
	p.Object["spec"] = spec
	return p
}

func gatewayTarget(name, section string) map[string]interface{} {
	ref := map[string]interface{}{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": name}
	if section != "" {
		ref["sectionName"] = section
	}
	return ref
}

// With usage metering on, usage is billed from identity headers the gateway
// writes. Each shared listener gets a policy that removes those headers from
// the caller's request before authentication runs, and makes Envoy generate
// every request id (the usage events' id).
func TestListenerHeaderPolicy_StripsCallerIdentityHeadersWhenMeteringIsOn(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	c := newFakeClientWithGateway(t)

	if err := (&ClusterTLSSingleton{Client: c, Config: cfg}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	for _, tc := range []struct{ gateway, listener string }{
		{"nebari-gateway", reconcilers.ExternalHTTPSListenerName},
		{"nebari-internal-gateway", reconcilers.InternalHTTPSListenerName},
	} {
		p := headerPolicy(t, c, tc.listener)
		if p == nil {
			t.Fatalf("no header policy for listener %s", tc.listener)
		}
		refs, _, _ := unstructured.NestedSlice(p.Object, "spec", "targetRefs")
		if len(refs) != 1 {
			t.Fatalf("%s targetRefs = %v, want one", tc.listener, refs)
		}
		ref := refs[0].(map[string]interface{})
		if ref["kind"] != "Gateway" || ref["name"] != tc.gateway || ref["sectionName"] != tc.listener {
			t.Errorf("%s targets %v, want Gateway %s section %s only", tc.listener, ref, tc.gateway, tc.listener)
		}
		removed, _, _ := unstructured.NestedStringSlice(p.Object, "spec", "headers", "earlyRequestHeaders", "remove")
		for _, h := range []string{"x-llm-client-id", "x-auth-user", "x-auth-subject", "x-auth-groups"} {
			if !slices.Contains(removed, h) {
				t.Errorf("%s does not remove %s from the caller's request (removes %v)", tc.listener, h, removed)
			}
		}
		if id, _, _ := unstructured.NestedString(p.Object, "spec", "headers", "requestID"); id != "Generate" {
			t.Errorf("%s requestID = %q, want Generate", tc.listener, id)
		}
	}
}

func TestListenerHeaderPolicy_NotCreatedWhenMeteringIsOff(t *testing.T) {
	c := newFakeClientWithGateway(t)

	if err := (&ClusterTLSSingleton{Client: c, Config: testOperatorConfig()}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName); p != nil {
		t.Error("header policy created with usage metering off")
	}
}

// Envoy Gateway applies one ClientTrafficPolicy per listener: a policy on the
// listener replaces one on its Gateway rather than merging with it. Where the
// cluster admin already has a policy covering the listener, the operator's
// would silently drop the admin's settings there, so it is not created (and
// one created earlier is removed). A policy for another listener only is no
// conflict.
func TestListenerHeaderPolicy_DefersToThePlatformsOwnPolicy(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	staleOurs := reconcilers.BuildListenerHeaderPolicy("nebari-gateway", "envoy-gateway-system", reconcilers.ExternalHTTPSListenerName)
	c := newFakeClientWithGateway(t,
		staleOurs,
		// Covers the whole external Gateway, so its llm-https listener too.
		platformPolicy("platform-timeouts", map[string]interface{}{
			"targetRefs": []interface{}{gatewayTarget("nebari-gateway", "")},
			"timeout":    map[string]interface{}{"http": map[string]interface{}{"idleTimeout": "5m"}},
		}),
		// Covers only another listener of the internal Gateway.
		platformPolicy("platform-http", map[string]interface{}{
			"targetRefs": []interface{}{gatewayTarget("nebari-internal-gateway", "http")},
		}),
	)

	if err := (&ClusterTLSSingleton{Client: c, Config: cfg}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName); p != nil {
		t.Error("header policy kept on a listener the platform's own policy covers")
	}
	if p := getUnstructured(t, c, clientTrafficPolicyGVK(), "platform-timeouts", "envoy-gateway-system"); p == nil {
		t.Error("the platform's policy was removed")
	}
	if p := headerPolicy(t, c, reconcilers.InternalHTTPSListenerName); p == nil {
		t.Error("no header policy on the internal listener, which no platform policy covers")
	}
}

// A policy can also select Gateways by label.
func TestListenerHeaderPolicy_DefersToAPlatformPolicySelectingTheGatewayByLabel(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	c := newFakeClientWithGateway(t,
		platformPolicy("platform-by-label", map[string]interface{}{
			"targetSelectors": []interface{}{map[string]interface{}{
				"group": "gateway.networking.k8s.io", "kind": "Gateway",
				"matchLabels": map[string]interface{}{"tier": "internal"},
			}},
		}),
	)
	gw := getUnstructured(t, c, gatewayGVK(), "nebari-internal-gateway", "envoy-gateway-system")
	gw.SetLabels(map[string]string{"tier": "internal"})
	if err := c.Update(context.Background(), gw); err != nil {
		t.Fatal(err)
	}

	if err := (&ClusterTLSSingleton{Client: c, Config: cfg}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if p := headerPolicy(t, c, reconcilers.InternalHTTPSListenerName); p != nil {
		t.Error("header policy created on a Gateway a platform policy selects by label")
	}
	if p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName); p == nil {
		t.Error("no header policy on the external listener, which the label does not select")
	}
}
