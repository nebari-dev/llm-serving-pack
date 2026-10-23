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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// +kubebuilder:rbac:groups=gateway.envoyproxy.io,resources=clienttrafficpolicies,verbs=get;list;watch;create;update;patch;delete

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

// ensureListenerHeaderPolicy creates or updates the header policy for one
// listener, unless a ClientTrafficPolicy the operator does not manage already
// covers that listener. Envoy Gateway applies a single ClientTrafficPolicy
// per listener (one on the listener replaces one on its Gateway, they do not
// merge), so adding the operator's would silently drop that policy's
// settings on the listener. The operator's own policy is then removed and
// the settings to add to the platform's policy are logged.
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
			log.Info("ClientTrafficPolicy CRD not installed; caller-sent identity headers are not removed on the shared listener",
				"listener", listenerName)
			return nil
		}
		return fmt.Errorf("listing ClientTrafficPolicies in %s: %w", gatewayNamespace, err)
	}

	desired := reconcilers.BuildListenerHeaderPolicy(gatewayName, gatewayNamespace, listenerName)
	for i := range policies.Items {
		p := &policies.Items[i]
		if isListenerHeaderPolicy(p) {
			continue
		}
		covers, err := policyCoversListener(p, gw, listenerName)
		if err != nil {
			return fmt.Errorf("reading ClientTrafficPolicy %s/%s: %w", p.GetNamespace(), p.GetName(), err)
		}
		if !covers {
			continue
		}
		log.Info("WARNING: a ClientTrafficPolicy the operator does not manage covers this shared listener, so the operator does not add its own; "+
			"add these settings to that policy so callers cannot choose the identity or request id usage is billed under",
			"policy", p.GetNamespace()+"/"+p.GetName(), "gateway", gatewayNamespace+"/"+gatewayName, "listener", listenerName,
			"headers.earlyRequestHeaders.remove", strings.Join(reconcilers.CallerIdentityHeaders, ","),
			"headers.requestID", "Generate")
		if err := r.Client.Delete(ctx, desired); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting ClientTrafficPolicy %s/%s: %w", gatewayNamespace, desired.GetName(), err)
		}
		return nil
	}
	return r.createOrUpdateUnstructured(ctx, desired)
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

// policyCoversListener reports whether ClientTrafficPolicy p applies to the
// named listener of gw: it targets gw as a whole or that listener, by name
// (targetRefs, or the deprecated targetRef) or by label (targetSelectors).
func policyCoversListener(p, gw *unstructured.Unstructured, listenerName string) (bool, error) {
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
		if section, _ := ref["sectionName"].(string); section == "" || section == listenerName {
			return true, nil
		}
	}
	selectors, _, _ := unstructured.NestedSlice(spec, "targetSelectors")
	for _, raw := range selectors {
		sel, ok := raw.(map[string]interface{})
		if !ok || !isGatewayKind(sel) {
			continue
		}
		var ls metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(sel, &ls); err != nil {
			return false, err
		}
		selector, err := metav1.LabelSelectorAsSelector(&ls)
		if err != nil {
			return false, err
		}
		if selector.Matches(labels.Set(gw.GetLabels())) {
			return true, nil
		}
	}
	return false, nil
}

func isGatewayKind(ref map[string]interface{}) bool {
	group, _ := ref["group"].(string)
	return ref["kind"] == "Gateway" && (group == "" || group == "gateway.networking.k8s.io")
}
