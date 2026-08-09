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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/llm-serving-pack/operator/internal/config"
	"github.com/nebari-dev/llm-serving-pack/operator/internal/controller/reconcilers"
)

// defaultClusterTLSResyncInterval is the background resync cadence for the
// shared-TLS reconciler. The reconciler is idempotent and cheap (at steady
// state it makes a handful of get requests and no writes), so a slow tick
// is fine; this is the backstop in case we miss a Gateway update event.
const defaultClusterTLSResyncInterval = 5 * time.Minute

// ClusterTLSSingleton is a manager.Runnable that reconciles cluster-wide
// resources backing shared-hostname routing:
//
//  1. A cert-manager Certificate in the operator's own namespace covering
//     llm.<baseDomain> and llm-internal.<baseDomain>. In
//     bring-your-own-certificate mode (Config.TLSSecretName set) the
//     Certificate is deleted instead and the user-provided Secret is used.
//  2. ReferenceGrants permitting the external and internal Gateways to
//     consume the resulting Secret across namespaces.
//  3. HTTPS listeners on the external and internal Gateways pointing at
//     the shared Secret (skipped when Config.ManageSharedListeners is false).
//
// Unlike LLMModelReconciler, which is driven per-CR, this reconciler owns
// cluster singletons. There is no LLMModel to key off and no OwnerReference
// to set on the cluster-scoped targets; uninstalling the operator leaves
// the Certificate and listeners in place on purpose so in-flight traffic
// keeps working. A simple resync loop is enough - the work is idempotent
// and the inputs change rarely.
type ClusterTLSSingleton struct {
	Client   client.Client
	Config   *config.OperatorConfig
	Interval time.Duration // resync cadence; 0 means use the default
}

// Start implements manager.Runnable. It runs one reconcile immediately and
// then on every tick until ctx is cancelled.
func (r *ClusterTLSSingleton) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("clustertls-singleton")

	interval := r.Interval
	if interval == 0 {
		interval = defaultClusterTLSResyncInterval
	}

	// Kick off an immediate reconcile; don't block Start on its outcome.
	if err := r.Reconcile(ctx); err != nil {
		log.Error(err, "initial shared-TLS reconcile failed; will retry on tick")
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := r.Reconcile(ctx); err != nil {
				log.Error(err, "shared-TLS reconcile failed; will retry on next tick")
			}
		}
	}
}

// NeedLeaderElection makes the singleton honour leader election when the
// manager has it enabled. Important: with two operator replicas both would
// otherwise race to patch the Gateways.
func (r *ClusterTLSSingleton) NeedLeaderElection() bool {
	return true
}

// Reconcile runs one full pass of the shared-TLS reconciliation. Exported
// so tests can drive it directly without needing the Runnable loop.
func (r *ClusterTLSSingleton) Reconcile(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("clustertls-singleton")

	if r.Config == nil {
		return fmt.Errorf("operator config is nil")
	}
	if r.Config.BaseDomain == "" {
		// Not an error worth alarming over - helm install without a base
		// domain is a misconfig the user will see reflected in the operator
		// logs and (later) in status.
		log.Info("skipping shared-TLS reconcile: baseDomain is empty")
		return nil
	}
	if r.Config.OperatorNamespace == "" {
		// POD_NAMESPACE is optional (envtest / webhook test mode); without
		// it we have no home namespace for the Certificate and Secret, so
		// we can't do anything useful. See design.md §Single-namespace
		// deployment model.
		log.Info("skipping shared-TLS reconcile: POD_NAMESPACE (OperatorNamespace) is empty")
		return nil
	}

	// 1. Ensure the TLS source. Default mode: an operator-managed
	// cert-manager Certificate. Bring-your-own-certificate mode
	// (Config.TLSSecretName set): the user pre-provisions the Secret, so
	// no Certificate is created and one previously managed by the
	// operator is deleted - leaving it would let cert-manager keep
	// renewing a secret nothing references anymore (or, if the user
	// reused the shared name, fight the user's data).
	if r.Config.TLSSecretName != "" {
		if err := r.deleteManagedCertificate(ctx); err != nil {
			return fmt.Errorf("deleting managed Certificate after switch to user-provided TLS secret: %w", err)
		}
		// Missing/invalid user secret is reported but not fatal: the
		// listeners stay pointed at the named secret so traffic recovers
		// the moment the user creates or fixes it.
		r.checkUserSecret(ctx)
	} else if err := r.ensureCertificate(ctx); err != nil {
		return fmt.Errorf("ensuring Certificate: %w", err)
	}

	// 2. Ensure ReferenceGrants for each distinct Gateway namespace.
	gatewayNamespaces := r.distinctGatewayNamespaces()
	for _, ns := range gatewayNamespaces {
		if ns == r.Config.OperatorNamespace {
			// Same-namespace ref needs no grant.
			continue
		}
		if err := r.ensureReferenceGrant(ctx, ns); err != nil {
			return fmt.Errorf("ensuring ReferenceGrant from %s: %w", ns, err)
		}
	}

	// 3. Patch Gateway listeners unless opted out.
	if !r.Config.ManageSharedListeners {
		log.V(1).Info("skipping Gateway listener patch: manageSharedListeners=false")
		return nil
	}

	if err := r.patchGatewayListener(
		ctx,
		r.Config.ExternalGatewayName,
		r.Config.ExternalGatewayNS,
		reconcilers.ExternalHTTPSListenerName,
		reconcilers.SharedExternalHostname(r.Config.BaseDomain),
	); err != nil {
		return fmt.Errorf("patching external Gateway listener: %w", err)
	}

	if err := r.patchGatewayListener(
		ctx,
		r.Config.InternalGatewayName,
		r.Config.InternalGatewayNS,
		reconcilers.InternalHTTPSListenerName,
		reconcilers.SharedInternalHostname(r.Config.BaseDomain),
	); err != nil {
		return fmt.Errorf("patching internal Gateway listener: %w", err)
	}

	return nil
}

func (r *ClusterTLSSingleton) ensureCertificate(ctx context.Context) error {
	desired, err := reconcilers.BuildSharedCertificate(
		r.Config.BaseDomain,
		r.Config.OperatorNamespace,
		r.Config.ClusterIssuerName,
	)
	if err != nil {
		return err
	}
	return r.createOrUpdateUnstructured(ctx, desired)
}

func (r *ClusterTLSSingleton) ensureReferenceGrant(ctx context.Context, fromNamespace string) error {
	desired := reconcilers.BuildSecretReferenceGrant(fromNamespace, r.Config.OperatorNamespace, r.sharedSecretName())
	return r.createOrUpdateUnstructured(ctx, desired)
}

// sharedSecretName returns the Secret the shared listeners terminate TLS
// with: the user-provided one in bring-your-own-certificate mode, the
// cert-manager-rendered one otherwise.
func (r *ClusterTLSSingleton) sharedSecretName() string {
	if r.Config.TLSSecretName != "" {
		return r.Config.TLSSecretName
	}
	return reconcilers.SharedTLSSecretName
}

// deleteManagedCertificate removes the operator-managed shared Certificate,
// if present. NotFound is the steady state in bring-your-own-certificate
// mode; a missing cert-manager CRD is also tolerated since BYO-cert
// clusters may not run cert-manager at all.
func (r *ClusterTLSSingleton) deleteManagedCertificate(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("clustertls-singleton")

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	cert.SetName(reconcilers.SharedTLSCertificateName)
	cert.SetNamespace(r.Config.OperatorNamespace)

	err := r.Client.Delete(ctx, cert)
	if err == nil {
		log.Info("deleted operator-managed shared Certificate (user-provided TLS secret is configured)",
			"certificate", reconcilers.SharedTLSCertificateName, "secret", r.Config.TLSSecretName)
		return nil
	}
	if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
		return nil
	}
	return err
}

// checkUserSecret verifies the user-provided Secret exists and is of type
// kubernetes.io/tls, logging (not failing) on problems. Mirrors the
// nebari-operator's behaviour for NebariApp routing.tls.secretName: the
// listener stays attached either way so Envoy picks the secret up as soon
// as it appears.
func (r *ClusterTLSSingleton) checkUserSecret(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("clustertls-singleton")

	secret := &unstructured.Unstructured{}
	secret.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"})
	key := types.NamespacedName{Name: r.Config.TLSSecretName, Namespace: r.Config.OperatorNamespace}
	if err := r.Client.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("user-provided TLS secret not found yet; shared listeners reference it and will serve once it is created",
				"secret", key)
			return
		}
		log.Error(err, "checking user-provided TLS secret", "secret", key)
		return
	}
	if secretType, _, _ := unstructured.NestedString(secret.Object, "type"); secretType != string("kubernetes.io/tls") {
		log.Info("user-provided TLS secret has the wrong type; expected kubernetes.io/tls",
			"secret", key, "type", secretType)
	}
}

// createOrUpdateUnstructured creates obj if missing, otherwise patches spec to
// match. Other fields (metadata labels, status) are left alone on existing
// resources to avoid fighting external controllers or admission webhooks.
func (r *ClusterTLSSingleton) createOrUpdateUnstructured(ctx context.Context, desired *unstructured.Unstructured) error {
	log := logf.FromContext(ctx).WithName("clustertls-singleton")

	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(desired.GroupVersionKind())
	key := types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}
	err := r.Client.Get(ctx, key, current)
	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating %s %s: %w", desired.GetKind(), key, err)
		}
		log.Info("created", "kind", desired.GetKind(), "name", key)
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting %s %s: %w", desired.GetKind(), key, err)
	}

	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	currentSpec, _, _ := unstructured.NestedMap(current.Object, "spec")
	if nestedSpecsEqual(desiredSpec, currentSpec) {
		return nil
	}
	if err := unstructured.SetNestedMap(current.Object, desiredSpec, "spec"); err != nil {
		return fmt.Errorf("setting spec: %w", err)
	}
	if err := r.Client.Update(ctx, current); err != nil {
		return fmt.Errorf("updating %s %s: %w", desired.GetKind(), key, err)
	}
	log.Info("updated", "kind", desired.GetKind(), "name", key)
	return nil
}

func (r *ClusterTLSSingleton) patchGatewayListener(
	ctx context.Context,
	gatewayName, gatewayNamespace, listenerName, hostname string,
) error {
	log := logf.FromContext(ctx).WithName("clustertls-singleton")

	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gatewayGVK())
	key := types.NamespacedName{Name: gatewayName, Namespace: gatewayNamespace}
	if err := r.Client.Get(ctx, key, gw); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("shared Gateway not found; skipping listener patch until it appears", "gateway", key)
			return nil
		}
		return fmt.Errorf("getting Gateway %s: %w", key, err)
	}

	changed, err := reconcilers.MergeHTTPSListener(
		gw,
		listenerName,
		hostname,
		r.sharedSecretName(),
		r.Config.OperatorNamespace,
	)
	if err != nil {
		return fmt.Errorf("merging listener: %w", err)
	}
	if !changed {
		return nil
	}
	if err := r.Client.Update(ctx, gw); err != nil {
		return fmt.Errorf("updating Gateway %s: %w", key, err)
	}
	log.Info("patched Gateway listener", "gateway", key, "listener", listenerName, "hostname", hostname)
	return nil
}

// distinctGatewayNamespaces returns the (deduplicated) namespaces of the
// external and internal shared Gateways.
func (r *ClusterTLSSingleton) distinctGatewayNamespaces() []string {
	if r.Config.ExternalGatewayNS == r.Config.InternalGatewayNS {
		return []string{r.Config.ExternalGatewayNS}
	}
	return []string{r.Config.ExternalGatewayNS, r.Config.InternalGatewayNS}
}

// gatewayGVK returns the GroupVersionKind for gateway.networking.k8s.io/v1 Gateway.
// v1 is GA in Gateway API 1.0; our minimum supported Envoy Gateway ships v1.
func gatewayGVK() (gvk schema.GroupVersionKind) {
	return schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}
}

// nestedSpecsEqual does a best-effort deep equality check on the spec blocks
// of two unstructured objects. We render YAML-style inputs, so comparing via
// fmt.Sprintf("%v", ...) with deterministic key ordering (which Go's map
// iteration does NOT give us, but apimachinery's unstructured happens to use
// JSON-encoded strings here) would be unsafe. Instead, walk the maps.
func nestedSpecsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if !valueEqual(av, bv) {
			return false
		}
	}
	return true
}

func valueEqual(a, b interface{}) bool {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok {
			return false
		}
		return nestedSpecsEqual(av, bv)
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok {
			return false
		}
		if len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !valueEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}
