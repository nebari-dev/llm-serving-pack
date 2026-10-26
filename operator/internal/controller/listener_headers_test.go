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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/record"
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

	if err := newTestSingleton(c, cfg).Reconcile(context.Background()); err != nil {
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

	if err := newTestSingleton(c, testOperatorConfig()).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName); p != nil {
		t.Error("header policy created with usage metering off")
	}
}

// Envoy Gateway applies one ClientTrafficPolicy per listener: a policy on the
// listener replaces the one on its Gateway for that listener, field by field
// nothing is merged. So where the cluster admin has a Gateway-wide policy (on
// Nebari, NIC's connection.bufferLimit: 50Mi), the operator's listener policy
// carries a copy of the admin's settings plus its own header settings: the
// listener keeps the admin's 50Mi buffer (without it Envoy falls back to
// 32KiB and large chat requests fail with 413) and caller-sent identity
// headers are still removed.
func TestListenerHeaderPolicy_CarriesTheGatewayWidePolicysSettings(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	c := newFakeClientWithGateway(t,
		platformPolicy("nebari-gateway-client-buffer", map[string]interface{}{
			"targetRefs": []interface{}{gatewayTarget("nebari-gateway", "")},
			"connection": map[string]interface{}{"bufferLimit": "50Mi"},
			"headers": map[string]interface{}{
				"requestID":           "PreserveOrGenerate",
				"earlyRequestHeaders": map[string]interface{}{"remove": []interface{}{"x-debug"}},
			},
		}),
		// Covers only another listener of the internal Gateway.
		platformPolicy("platform-http", map[string]interface{}{
			"targetRefs": []interface{}{gatewayTarget("nebari-internal-gateway", "http")},
			"timeout":    map[string]interface{}{"http": map[string]interface{}{"idleTimeout": "5m"}},
		}),
	)

	if err := newTestSingleton(c, cfg).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName)
	if p == nil {
		t.Fatal("no header policy on the listener the Gateway-wide policy covers")
	}
	if got, _, _ := unstructured.NestedString(p.Object, "spec", "connection", "bufferLimit"); got != "50Mi" {
		t.Errorf("connection.bufferLimit = %q, want the Gateway-wide policy's 50Mi", got)
	}
	refs, _, _ := unstructured.NestedSlice(p.Object, "spec", "targetRefs")
	if len(refs) != 1 || refs[0].(map[string]interface{})["sectionName"] != reconcilers.ExternalHTTPSListenerName {
		t.Errorf("targetRefs = %v, want the llm-https listener only", refs)
	}
	removed, _, _ := unstructured.NestedStringSlice(p.Object, "spec", "headers", "earlyRequestHeaders", "remove")
	for _, h := range append([]string{"x-debug"}, reconcilers.CallerIdentityHeaders...) {
		if !slices.Contains(removed, h) {
			t.Errorf("does not remove %s (removes %v)", h, removed)
		}
	}
	if id, _, _ := unstructured.NestedString(p.Object, "spec", "headers", "requestID"); id != "Generate" {
		t.Errorf("requestID = %q, want Generate", id)
	}
	if p := getUnstructured(t, c, clientTrafficPolicyGVK(), "nebari-gateway-client-buffer", "envoy-gateway-system"); p == nil {
		t.Error("the platform's policy was removed")
	}

	// The internal listener: the other policy covers only the http listener.
	internal := headerPolicy(t, c, reconcilers.InternalHTTPSListenerName)
	if internal == nil {
		t.Fatal("no header policy on the internal listener")
	}
	if _, found, _ := unstructured.NestedMap(internal.Object, "spec", "timeout"); found {
		t.Error("internal listener policy copied a policy that covers another listener only")
	}
}

// A change to the admin's policy reaches the operator's copy.
func TestListenerHeaderPolicy_FollowsChangesToTheGatewayWidePolicy(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	c := newFakeClientWithGateway(t,
		platformPolicy("nebari-gateway-client-buffer", map[string]interface{}{
			"targetRefs": []interface{}{gatewayTarget("nebari-gateway", "")},
			"connection": map[string]interface{}{"bufferLimit": "50Mi"},
		}),
	)
	r := newTestSingleton(c, cfg)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	platform := getUnstructured(t, c, clientTrafficPolicyGVK(), "nebari-gateway-client-buffer", "envoy-gateway-system")
	_ = unstructured.SetNestedField(platform.Object, "100Mi", "spec", "connection", "bufferLimit")
	if err := c.Update(context.Background(), platform); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName)
	if p == nil {
		t.Fatal("no header policy")
	}
	if got, _, _ := unstructured.NestedString(p.Object, "spec", "connection", "bufferLimit"); got != "100Mi" {
		t.Errorf("connection.bufferLimit = %q after the platform policy changed to 100Mi", got)
	}
}

// Of several Gateway-wide policies Envoy Gateway applies the oldest (then by
// namespace/name); the others are marked Conflicted. The copy is of that one.
func TestListenerHeaderPolicy_CopiesThePolicyEnvoyGatewayApplies(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	older := platformPolicy("z-older", map[string]interface{}{
		"targetRefs": []interface{}{gatewayTarget("nebari-gateway", "")},
		"connection": map[string]interface{}{"bufferLimit": "50Mi"},
	})
	older.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	newer := platformPolicy("a-newer", map[string]interface{}{
		"targetRefs": []interface{}{gatewayTarget("nebari-gateway", "")},
		"connection": map[string]interface{}{"bufferLimit": "1Mi"},
	})
	newer.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	c := newFakeClientWithGateway(t, newer, older)

	if err := newTestSingleton(c, cfg).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName)
	if got, _, _ := unstructured.NestedString(p.Object, "spec", "connection", "bufferLimit"); got != "50Mi" {
		t.Errorf("connection.bufferLimit = %q, want the oldest policy's 50Mi", got)
	}
}

// A policy can also select Gateways by label; it is copied the same way.
func TestListenerHeaderPolicy_CarriesAPolicySelectingTheGatewayByLabel(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	c := newFakeClientWithGateway(t,
		platformPolicy("platform-by-label", map[string]interface{}{
			"targetSelectors": []interface{}{map[string]interface{}{
				"group": "gateway.networking.k8s.io", "kind": "Gateway",
				"matchLabels": map[string]interface{}{"tier": "internal"},
			}},
			"timeout": map[string]interface{}{"http": map[string]interface{}{"idleTimeout": "5m"}},
		}),
	)
	gw := getUnstructured(t, c, gatewayGVK(), "nebari-internal-gateway", "envoy-gateway-system")
	gw.SetLabels(map[string]string{"tier": "internal"})
	if err := c.Update(context.Background(), gw); err != nil {
		t.Fatal(err)
	}

	if err := newTestSingleton(c, cfg).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	internal := headerPolicy(t, c, reconcilers.InternalHTTPSListenerName)
	if got, _, _ := unstructured.NestedString(internal.Object, "spec", "timeout", "http", "idleTimeout"); got != "5m" {
		t.Errorf("internal listener idleTimeout = %q, want the selecting policy's 5m", got)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(internal.Object, "spec", "targetSelectors"); found {
		t.Error("the copy kept the platform policy's targetSelectors")
	}
	external := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName)
	if _, found, _ := unstructured.NestedMap(external.Object, "spec", "timeout"); found {
		t.Error("external listener policy copied a policy whose label does not select its Gateway")
	}
}

// A policy the operator does not manage that targets the listener itself
// (sectionName) cannot share it: Envoy Gateway applies only the older of two
// such policies. The operator removes its own, so the admin's applies, and
// says loudly that caller-sent identity headers are not removed there: a
// Warning Event on the Gateway, as well as the log.
func TestListenerHeaderPolicy_WarnsWhenAnotherPolicyTargetsTheListener(t *testing.T) {
	cfg := testOperatorConfig()
	cfg.UsageMeteringEnabled = true
	c := newFakeClientWithGateway(t,
		reconcilers.BuildListenerHeaderPolicy("nebari-gateway", "envoy-gateway-system", reconcilers.ExternalHTTPSListenerName, nil),
		platformPolicy("platform-llm", map[string]interface{}{
			"targetRefs": []interface{}{gatewayTarget("nebari-gateway", reconcilers.ExternalHTTPSListenerName)},
		}),
	)
	recorder := record.NewFakeRecorder(10)
	r := &ClusterTLSSingleton{Client: c, Config: cfg, Recorder: recorder}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if p := headerPolicy(t, c, reconcilers.ExternalHTTPSListenerName); p != nil {
		t.Error("header policy kept on a listener another policy targets")
	}
	if p := headerPolicy(t, c, reconcilers.InternalHTTPSListenerName); p == nil {
		t.Error("no header policy on the internal listener, which no other policy targets")
	}
	select {
	case e := <-recorder.Events:
		if !strings.HasPrefix(e, "Warning ") || !strings.Contains(e, "platform-llm") || !strings.Contains(e, "x-auth-groups") {
			t.Errorf("event = %q, want a Warning naming the policy and the headers to remove", e)
		}
	default:
		t.Error("no Event recorded")
	}
}
