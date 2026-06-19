/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconcilers

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Shared-TLS constants. The operator maintains a single Certificate covering
// both shared hostnames and a single Secret holding the rendered cert; one
// name for each keeps cross-namespace references (from the Gateways and
// ReferenceGrants) unambiguous. See the Architecture docs for the full picture:
// https://nebari-dev.github.io/nebari-llm-serving-pack/architecture/
const (
	SharedTLSCertificateName = "nebari-llm-shared-tls"
	SharedTLSSecretName      = "nebari-llm-shared-tls"

	// Listener names used on the shared Gateways. The reconciler matches
	// listeners by name so it can idempotently update its own listener
	// without clobbering unrelated listeners on the same Gateway.
	ExternalHTTPSListenerName = "llm-https"
	InternalHTTPSListenerName = "llm-internal-https"
)

// sharedTLSLabels returns the labels applied to resources managed by the
// shared-TLS reconciler. These are pack-scoped (not tied to any LLMModel).
func sharedTLSLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "nebari-llm-operator",
		"app.kubernetes.io/name":       "shared-tls",
		"app.kubernetes.io/component":  "routing",
	}
}

// BuildSharedCertificate returns a cert-manager.io/v1 Certificate that covers
// both shared hostnames. The Certificate lives in the operator's own namespace;
// cert-manager writes the issued cert into a Secret of the same name in the
// same namespace. baseDomain must be non-empty - an empty value would produce
// SANs like "llm." which cert-manager would reject and which could never be
// issued via HTTP-01 anyway.
//
// References:
//   - Certificate spec reference: https://cert-manager.io/docs/reference/api-docs/#cert-manager.io/v1.CertificateSpec
//   - HTTP-01 challenge (no wildcards): https://cert-manager.io/docs/configuration/acme/http01/
func BuildSharedCertificate(baseDomain, namespace, clusterIssuerName string) (*unstructured.Unstructured, error) {
	if baseDomain == "" {
		return nil, fmt.Errorf("baseDomain must be non-empty to build the shared TLS Certificate")
	}
	if namespace == "" {
		return nil, fmt.Errorf("namespace must be non-empty")
	}
	if clusterIssuerName == "" {
		return nil, fmt.Errorf("clusterIssuerName must be non-empty")
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]interface{}{
				"name":      SharedTLSCertificateName,
				"namespace": namespace,
				"labels":    labelsToInterface(sharedTLSLabels()),
			},
			"spec": map[string]interface{}{
				"secretName": SharedTLSSecretName,
				"dnsNames": []interface{}{
					SharedExternalHostname(baseDomain),
					SharedInternalHostname(baseDomain),
				},
				"issuerRef": map[string]interface{}{
					"kind":  "ClusterIssuer",
					"name":  clusterIssuerName,
					"group": "cert-manager.io",
				},
			},
		},
	}, nil
}

// BuildSecretReferenceGrant returns a gateway.networking.k8s.io/v1beta1
// ReferenceGrant that allows Gateways in fromNamespace to reference the
// shared-TLS Secret in secretNamespace. The Gateway API requires a
// ReferenceGrant for any cross-namespace Secret reference on a Gateway
// listener.
//
// One ReferenceGrant per source namespace keeps the grants minimally scoped.
// The ReferenceGrant itself must live in the target namespace (where the
// Secret is), per the Gateway API spec.
//
// Reference:
//   - ReferenceGrant spec: https://gateway-api.sigs.k8s.io/api-types/referencegrant/
func BuildSecretReferenceGrant(fromNamespace, secretNamespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1beta1",
			"kind":       "ReferenceGrant",
			"metadata": map[string]interface{}{
				"name":      "nebari-llm-shared-tls-from-" + fromNamespace,
				"namespace": secretNamespace,
				"labels":    labelsToInterface(sharedTLSLabels()),
			},
			"spec": map[string]interface{}{
				"from": []interface{}{
					map[string]interface{}{
						"group":     "gateway.networking.k8s.io",
						"kind":      "Gateway",
						"namespace": fromNamespace,
					},
				},
				"to": []interface{}{
					map[string]interface{}{
						"group": "",
						"kind":  "Secret",
						"name":  SharedTLSSecretName,
					},
				},
			},
		},
	}
}

// buildHTTPSListener returns an HTTPS listener spec (as a map[string]interface{})
// ready to be appended to Gateway.spec.listeners.
func buildHTTPSListener(name, hostname, secretName, secretNamespace string) map[string]interface{} {
	return map[string]interface{}{
		"name":     name,
		"protocol": "HTTPS",
		"port":     int64(443),
		"hostname": hostname,
		"tls": map[string]interface{}{
			"mode": "Terminate",
			"certificateRefs": []interface{}{
				map[string]interface{}{
					"kind":      "Secret",
					"name":      secretName,
					"namespace": secretNamespace,
				},
			},
		},
		"allowedRoutes": map[string]interface{}{
			"namespaces": map[string]interface{}{
				"from": "All",
			},
		},
	}
}

// MergeHTTPSListener ensures the given Gateway has an HTTPS listener named
// listenerName that terminates TLS for hostname using the shared-TLS Secret.
// It matches existing listeners by name: if one exists with that name, its
// protocol/port/hostname/tls/allowedRoutes are overwritten to the desired
// values; if no listener with that name exists, one is appended. Listeners
// with other names are left untouched - the shared Gateways already carry
// listeners for the bare domain, argocd, keycloak, etc. that the operator
// must not clobber.
//
// Returns true if the Gateway was modified (needs a write back), false if
// it was already in the desired state.
//
// The function mutates gateway in place.
//
// Reference:
//   - Gateway spec: https://gateway-api.sigs.k8s.io/api-types/gateway/
func MergeHTTPSListener(gateway *unstructured.Unstructured, listenerName, hostname, secretName, secretNamespace string) (bool, error) {
	if gateway == nil {
		return false, fmt.Errorf("gateway must be non-nil")
	}

	desired := buildHTTPSListener(listenerName, hostname, secretName, secretNamespace)

	listeners, found, err := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	if err != nil {
		return false, fmt.Errorf("reading spec.listeners: %w", err)
	}
	if !found {
		listeners = []interface{}{}
	}

	// Scan for an existing listener by name.
	for i, raw := range listeners {
		l, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := l["name"].(string)
		if name != listenerName {
			continue
		}
		// Replace fully; equality check decides whether a write is needed.
		if listenersEqual(l, desired) {
			return false, nil
		}
		listeners[i] = desired
		if err := unstructured.SetNestedSlice(gateway.Object, listeners, "spec", "listeners"); err != nil {
			return false, fmt.Errorf("writing spec.listeners: %w", err)
		}
		return true, nil
	}

	// Not found; append.
	listeners = append(listeners, desired)
	if err := unstructured.SetNestedSlice(gateway.Object, listeners, "spec", "listeners"); err != nil {
		return false, fmt.Errorf("writing spec.listeners: %w", err)
	}
	return true, nil
}

// listenersEqual reports whether two listener maps represent the same listener.
// The comparison is deep-value over the fields MergeHTTPSListener sets; any
// other fields (e.g. future Gateway API additions a user might hand-edit onto
// our listener) are not considered, which means the next reconcile would
// overwrite them. That tradeoff is intentional - the listener is operator-owned.
func listenersEqual(a, b map[string]interface{}) bool {
	keys := []string{"name", "protocol", "port", "hostname"}
	for _, k := range keys {
		if fmt.Sprintf("%v", a[k]) != fmt.Sprintf("%v", b[k]) {
			return false
		}
	}
	// TLS block.
	at, _ := a["tls"].(map[string]interface{})
	bt, _ := b["tls"].(map[string]interface{})
	if fmt.Sprintf("%v", at["mode"]) != fmt.Sprintf("%v", bt["mode"]) {
		return false
	}
	aRefs, _ := at["certificateRefs"].([]interface{})
	bRefs, _ := bt["certificateRefs"].([]interface{})
	if len(aRefs) != len(bRefs) {
		return false
	}
	for i := range aRefs {
		ar, _ := aRefs[i].(map[string]interface{})
		br, _ := bRefs[i].(map[string]interface{})
		for _, k := range []string{"kind", "name", "namespace"} {
			if fmt.Sprintf("%v", ar[k]) != fmt.Sprintf("%v", br[k]) {
				return false
			}
		}
	}
	// allowedRoutes.namespaces.from
	aAllowed, _ := a["allowedRoutes"].(map[string]interface{})
	bAllowed, _ := b["allowedRoutes"].(map[string]interface{})
	aNS, _ := aAllowed["namespaces"].(map[string]interface{})
	bNS, _ := bAllowed["namespaces"].(map[string]interface{})
	return fmt.Sprintf("%v", aNS["from"]) == fmt.Sprintf("%v", bNS["from"])
}
