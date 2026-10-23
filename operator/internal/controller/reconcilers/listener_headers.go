/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconcilers

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
func BuildListenerHeaderPolicy(gatewayName, gatewayNamespace, listenerName string) *unstructured.Unstructured {
	remove := make([]interface{}, 0, len(CallerIdentityHeaders))
	for _, h := range CallerIdentityHeaders {
		remove = append(remove, h)
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.envoyproxy.io/v1alpha1",
			"kind":       "ClientTrafficPolicy",
			"metadata": map[string]interface{}{
				"name":      ListenerHeaderPolicyName(listenerName),
				"namespace": gatewayNamespace,
				"labels":    labelsToInterface(sharedTLSLabels()),
			},
			"spec": map[string]interface{}{
				"targetRefs": []interface{}{
					map[string]interface{}{
						"group":       "gateway.networking.k8s.io",
						"kind":        "Gateway",
						"name":        gatewayName,
						"sectionName": listenerName,
					},
				},
				"headers": map[string]interface{}{
					"requestID": "Generate",
					"earlyRequestHeaders": map[string]interface{}{
						"remove": remove,
					},
				},
			},
		},
	}
}
