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
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// +kubebuilder:rbac:groups=gateway.envoyproxy.io,resources=clienttrafficpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func clientTrafficPolicyGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "ClientTrafficPolicy"}
}

// ensureListenerHeaderPolicies gives each shared listener the
// ClientTrafficPolicy that removes caller-sent identity headers
// (reconcilers.BuildListenerHeaderPolicy) while usage metering is on: usage
// is billed from those headers. With metering off nothing is done, and a
// policy created earlier is left in place (it only removes headers the
// gateway itself sets, and keeps Envoy's default request ids).
func (r *ClusterTLSSingleton) ensureListenerHeaderPolicies(ctx context.Context) error {
	if !r.Config.UsageMeteringEnabled {
		return nil
	}
	if err := r.ensureListenerHeaderPolicy(ctx, r.Config.ExternalGatewayName, r.Config.ExternalGatewayNS, reconcilers.ExternalHTTPSListenerName); err != nil {
		return fmt.Errorf("external listener header policy: %w", err)
	}
	if err := r.ensureListenerHeaderPolicy(ctx, r.Config.InternalGatewayName, r.Config.InternalGatewayNS, reconcilers.InternalHTTPSListenerName); err != nil {
		return fmt.Errorf("internal listener header policy: %w", err)
	}
	return nil
}

// SetupClientTrafficPolicyWatch re-runs ensureListenerHeaderPolicies when a
// ClientTrafficPolicy in a shared Gateway's namespace changes, so a change to
// a Gateway-wide policy reaches the listener's copy of it in seconds rather
// than on the next resync. Register it only with usage metering on: it needs
// the ClientTrafficPolicy RBAC the chart grants in that case.
func (r *ClusterTLSSingleton) SetupClientTrafficPolicyWatch(mgr ctrl.Manager) error {
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(clientTrafficPolicyGVK())
	namespaces := map[string]bool{}
	for _, ns := range r.distinctGatewayNamespaces() {
		namespaces[ns] = true
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("listener-header-policies").
		For(policy, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return namespaces[o.GetNamespace()]
		}))).
		Complete(reconcile.Func(func(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return reconcile.Result{}, r.ensureListenerHeaderPolicies(ctx)
		}))
}

// ensureListenerHeaderPolicy creates or updates the header policy for one
// listener. Envoy Gateway applies a single ClientTrafficPolicy per listener:
// one targeting the listener (sectionName) replaces one on its whole Gateway
// there, and they do not merge. So:
//
//   - a Gateway-wide policy the operator does not manage (on Nebari, NIC's
//     connection buffer limit) is copied into the listener's policy, which
//     adds the header settings (reconcilers.BuildListenerHeaderPolicy);
//   - a policy the operator does not manage that targets the listener itself
//     cannot share it (Envoy Gateway keeps only the older one), so the
//     operator removes its own and reports, on the Gateway as a Warning Event
//     and in the log, that caller-sent identity headers are not removed there
//     and which settings to add to that policy.
func (r *ClusterTLSSingleton) ensureListenerHeaderPolicy(ctx context.Context, gatewayName, gatewayNamespace, listenerName string) error {
	log := logf.FromContext(ctx).WithName("clustertls-singleton")

	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK())
	if err := r.Client.Get(ctx, types.NamespacedName{Name: gatewayName, Namespace: gatewayNamespace}, gw); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // patchGatewayListener logs the missing Gateway
		}
		return fmt.Errorf("getting Gateway %s/%s: %w", gatewayNamespace, gatewayName, err)
	}

	policies := &unstructured.UnstructuredList{}
	policies.SetGroupVersionKind(clientTrafficPolicyGVK().GroupVersion().WithKind("ClientTrafficPolicyList"))
	if err := r.Client.List(ctx, policies, client.InNamespace(gatewayNamespace)); err != nil {
		if apimeta.IsNoMatchError(err) {
			r.warnUnprotected(ctx, gw, listenerName, "the ClientTrafficPolicy CRD is not installed")
			return nil
		}
		return fmt.Errorf("listing ClientTrafficPolicies in %s: %w", gatewayNamespace, err)
	}

	var gatewayWide []*unstructured.Unstructured
	for i := range policies.Items {
		p := &policies.Items[i]
		if isListenerHeaderPolicy(p) {
			continue
		}
		scope, err := policyScopeForListener(p, gw, listenerName)
		if err != nil {
			return fmt.Errorf("reading ClientTrafficPolicy %s/%s: %w", p.GetNamespace(), p.GetName(), err)
		}
		switch scope {
		case scopeListener:
			r.warnUnprotected(ctx, gw, listenerName, fmt.Sprintf("ClientTrafficPolicy %s/%s targets the listener and Envoy Gateway applies one policy per listener; "+
				"add headers.earlyRequestHeaders.remove [%s] and headers.requestID Generate to that policy",
				p.GetNamespace(), p.GetName(), strings.Join(reconcilers.CallerIdentityHeaders, ", ")))
			stale := reconcilers.BuildListenerHeaderPolicy(gatewayName, gatewayNamespace, listenerName, nil)
			if err := r.Client.Delete(ctx, stale); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting ClientTrafficPolicy %s/%s: %w", gatewayNamespace, stale.GetName(), err)
			}
			return nil
		case scopeGateway:
			gatewayWide = append(gatewayWide, p)
		}
	}

	var inherited map[string]interface{}
	if applied := appliedGatewayWidePolicy(gatewayWide); applied != nil {
		inherited, _, _ = unstructured.NestedMap(applied.Object, "spec")
		log.V(1).Info("listener header policy carries the Gateway-wide ClientTrafficPolicy's settings",
			"policy", applied.GetNamespace()+"/"+applied.GetName(), "listener", listenerName)
	}
	return r.createOrUpdateUnstructured(ctx, reconcilers.BuildListenerHeaderPolicy(gatewayName, gatewayNamespace, listenerName, inherited))
}

// warnUnprotected reports that caller-sent identity headers are not removed
// on a shared listener, so usage on it can be billed to an identity the
// caller chose: a Warning Event on the Gateway and an error in the log.
func (r *ClusterTLSSingleton) warnUnprotected(ctx context.Context, gw *unstructured.Unstructured, listenerName, why string) {
	msg := fmt.Sprintf("usage metering: caller-sent identity headers are NOT removed on listener %s, so callers can choose the identity usage is billed under: %s",
		listenerName, why)
	logf.FromContext(ctx).WithName("clustertls-singleton").Error(nil, msg,
		"gateway", gw.GetNamespace()+"/"+gw.GetName(), "listener", listenerName)
	r.Recorder.Event(gw, corev1.EventTypeWarning, "UsageHeadersNotProtected", msg)
}

// appliedGatewayWidePolicy returns the one of several Gateway-wide
// ClientTrafficPolicies Envoy Gateway applies: it sorts them by creation
// time, then namespace/name, and marks all but the first Conflicted.
func appliedGatewayWidePolicy(policies []*unstructured.Unstructured) *unstructured.Unstructured {
	if len(policies) == 0 {
		return nil
	}
	return slices.MinFunc(policies, func(a, b *unstructured.Unstructured) int {
		ta, tb := a.GetCreationTimestamp(), b.GetCreationTimestamp()
		if !ta.Equal(&tb) {
			if ta.Before(&tb) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.GetNamespace()+"/"+a.GetName(), b.GetNamespace()+"/"+b.GetName())
	})
}

// isListenerHeaderPolicy reports whether p is one of the operator's own
// header policies (one per shared listener, possibly on the same Gateway).
func isListenerHeaderPolicy(p *unstructured.Unstructured) bool {
	if p.GetLabels()["app.kubernetes.io/managed-by"] != "nebari-llm-operator" {
		return false
	}
	name := p.GetName()
	return name == reconcilers.ListenerHeaderPolicyName(reconcilers.ExternalHTTPSListenerName) ||
		name == reconcilers.ListenerHeaderPolicyName(reconcilers.InternalHTTPSListenerName)
}

// policyScope is how a ClientTrafficPolicy applies to a listener.
type policyScope int

const (
	scopeNone     policyScope = iota // does not apply to the listener
	scopeGateway                     // applies to the listener's whole Gateway
	scopeListener                    // targets the listener itself (sectionName)
)

// policyScopeForListener reports how ClientTrafficPolicy p applies to the
// named listener of gw: by targeting that listener (targetRefs, or the
// deprecated targetRef, with its sectionName), or the Gateway as a whole by
// name or by label (targetSelectors). A listener target wins, as it does in
// Envoy Gateway.
func policyScopeForListener(p, gw *unstructured.Unstructured, listenerName string) (policyScope, error) {
	scope := scopeNone
	spec, _, _ := unstructured.NestedMap(p.Object, "spec")
	refs, _, _ := unstructured.NestedSlice(spec, "targetRefs")
	if ref, ok, _ := unstructured.NestedMap(spec, "targetRef"); ok {
		refs = append(refs, ref)
	}
	for _, raw := range refs {
		ref, ok := raw.(map[string]interface{})
		if !ok || !isGatewayKind(ref) || ref["name"] != gw.GetName() {
			continue
		}
		switch section, _ := ref["sectionName"].(string); section {
		case listenerName:
			return scopeListener, nil
		case "":
			scope = scopeGateway
		}
	}
	if scope == scopeGateway {
		return scope, nil
	}
	selectors, _, _ := unstructured.NestedSlice(spec, "targetSelectors")
	for _, raw := range selectors {
		sel, ok := raw.(map[string]interface{})
		if !ok || !isGatewayKind(sel) {
			continue
		}
		var ls metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(sel, &ls); err != nil {
			return scopeNone, err
		}
		selector, err := metav1.LabelSelectorAsSelector(&ls)
		if err != nil {
			return scopeNone, err
		}
		if selector.Matches(labels.Set(gw.GetLabels())) {
			return scopeGateway, nil
		}
	}
	return scopeNone, nil
}

func isGatewayKind(ref map[string]interface{}) bool {
	group, _ := ref["group"].(string)
	return ref["kind"] == "Gateway" && (group == "" || group == "gateway.networking.k8s.io")
}
