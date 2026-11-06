/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconcilers

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// CallerIdentityHeaders are the request headers the gateway writes once a
// caller is authenticated: the API key's client ID (apiKeyAuth) and the
// token's claims (jwt claimToHeaders). Usage metering bills from them, so a
// value the caller sent must never survive to the access log.
var CallerIdentityHeaders = []string{apiKeyClientIDHeader, "x-auth-user", "x-auth-subject", "x-auth-groups"}

// ListenerHeaderPolicyName is the name of the ClientTrafficPolicy the
// operator manages for a shared listener.
func ListenerHeaderPolicyName(listenerName string) string {
	return listenerName + "-client-headers"
}

// BuildListenerHeaderPolicy returns an Envoy Gateway ClientTrafficPolicy for
// one shared listener that:
//
//   - removes CallerIdentityHeaders from the caller's request. Early request
//     headers are applied before routing and before any HTTP filter, so the
//     authentication filters then write the only values present.
//   - makes Envoy generate every x-request-id, replacing the caller's. The
//     usage events' ids are request ids, and OpenMeter keeps only the first
//     event with an id, so a caller choosing ids could have usage dropped.
//     Generate is Envoy Gateway's default; it is set so it holds whatever
//     else the listener's defaults become.
//
// inherited is the spec of the Gateway-wide ClientTrafficPolicy that would
// otherwise apply to the listener, or nil. Envoy Gateway applies one policy
// per listener and does not merge them, so the listener's policy starts from
// a copy of those settings (everything but the targets) and adds the two
// above; early headers the inherited policy removes stay removed. Keeping
// inherited L4 settings matters: listeners that share a port share one Envoy
// listener, whose connection buffer limit comes from whichever of them Envoy
// Gateway translates first, so every policy on the port must agree on it.
func BuildListenerHeaderPolicy(gatewayName, gatewayNamespace, listenerName string, inherited map[string]interface{}) *unstructured.Unstructured {
	spec := map[string]interface{}{}
	if inherited != nil {
		spec = runtime.DeepCopyJSON(inherited)
	}
	delete(spec, "targetRef")
	delete(spec, "targetRefs")
	delete(spec, "targetSelectors")
	spec["targetRefs"] = []interface{}{
		map[string]interface{}{
			"group":       "gateway.networking.k8s.io",
			"kind":        "Gateway",
			"name":        gatewayName,
			"sectionName": listenerName,
		},
	}

	headers, _, _ := unstructured.NestedMap(spec, "headers")
	if headers == nil {
		headers = map[string]interface{}{}
	}
	headers["requestID"] = "Generate"
	early, _, _ := unstructured.NestedMap(headers, "earlyRequestHeaders")
	if early == nil {
		early = map[string]interface{}{}
	}
	remove, _, _ := unstructured.NestedStringSlice(early, "remove")
	for _, h := range CallerIdentityHeaders {
		if !slices.Contains(remove, h) {
			remove = append(remove, h)
		}
	}
	removeList := make([]interface{}, 0, len(remove))
	for _, h := range remove {
		removeList = append(removeList, h)
	}
	early["remove"] = removeList
	headers["earlyRequestHeaders"] = early
	spec["headers"] = headers

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.envoyproxy.io/v1alpha1",
			"kind":       "ClientTrafficPolicy",
			"metadata": map[string]interface{}{
				"name":      ListenerHeaderPolicyName(listenerName),
				"namespace": gatewayNamespace,
				"labels":    labelsToInterface(sharedTLSLabels()),
			},
			"spec": spec,
		},
	}
}
