---
title: Architecture
---
This page explains how the Nebari LLM serving pack is designed: what components it deploys, how the operator reconciles `LLMModel` resources, how the two authentication paths work, how the key manager operates, and what security guarantees the design provides.

For configuration knobs (Helm values, CRD fields), see [Configuration](/configuration/).
For installation steps, see [Installation](/installation/).
For model storage detail, see [Shared Storage](/shared-storage/).
To get a model running quickly, see [Quickstart](/quickstart/).

---

## Overview

The pack deploys a Go operator that watches a custom `LLMModel` CRD. Admins apply one `LLMModel` per model they want to serve. The operator handles everything downstream: model storage, vLLM serving pods, inference scheduling, routing, and access control.

An optional key manager service gives users a web UI to generate API keys for external access. Envoy AI Gateway provides token counting, rate limiting, and protocol normalization on both external and internal endpoints.

---

## Goals

- Admins deploy the pack once via ArgoCD, then declare models as `LLMModel` CRs
- Per-model access control via OIDC groups, generic for any OIDC provider, tested against Keycloak
- Two auth paths: API keys for external consumers, JWT bearer tokens for in-cluster users
- Model loading from HuggingFace (default) or OCI/modelcar images
- Intelligent inference scheduling via llm-d's `InferencePool` and EPP
- Token counting and rate limiting on both endpoints via Envoy AI Gateway
- NetworkPolicy enforcement: model pods are only reachable through the Gateway

## Non-goals for v0.1

- Prefill/decode disaggregation
- KV cache offloading
- Multi-accelerator support (AMD, Intel, etc.) - NVIDIA only initially
- Usage billing or cost chargeback
- Scale-to-zero autoscaling
- Per-key rate limiting or token quotas
- API key expiration and group-change revocation (the periodic audit is scaffolded, but the userinfo lookup it depends on is a stub, so no key is revoked yet)
- Team-level shared API keys
- Kubernetes image volumes for OCI models (init-container copy is the baseline)

---

## Components deployed by the Helm chart

Installing the chart (pack install) deploys:

1. **LLMModel CRD** - the `llm.nebari.dev/v1alpha1` custom resource definition
2. **LLM operator** - a Go controller (kubebuilder/controller-runtime) that watches `LLMModel` CRs in its own namespace
3. **Key manager** (conditional, `keyManager.enabled`) - a web UI and REST API behind a `NebariApp` with Keycloak/OIDC auth
4. **Envoy AI Gateway** (conditional, `envoyAIGateway.install`) - the controller and CRDs; when `false`, assumes pre-installed

The chart creates the operator namespace and labels it `nebari.dev/managed=true` (gated on `createNamespace: true`, default on).

---

## Resources created per LLMModel

All resources live in the `LLMModel`'s own namespace, which (per the validating webhook) must be the operator's namespace.

| Resource | Purpose |
|---|---|
| PVC | Model storage (HuggingFace source, pvc storage type) |
| Deployment (vLLM) | Model serving pods with init container for preloading |
| Service | ClusterIP for the vLLM pods |
| ServiceAccount | Pod identity |
| NetworkPolicy | Default-deny ingress, allow only Gateway + EPP + Prometheus |
| InferencePool + EPP | Intelligent inference scheduling |
| AIGatewayRoute (external) | External endpoint with token counting, rate limiting |
| AIGatewayRoute (internal) | Internal endpoint with token counting, rate limiting |
| SecurityPolicy (external) | apiKeyAuth referencing the per-model Secret (same namespace) plus a deny-by-default authorization allow-list of that model's client IDs |
| SecurityPolicy (internal) | JWT validation with group claim matching against `access.groups` |
| Secret (API keys) | Per-model API key store (created by operator, data managed by key manager) |
| ConfigMap (key metadata) | Per-model metadata for API keys (creator, timestamp, description) |
| PodMonitor | Prometheus metrics scraping (when monitoring enabled) |

### Resource ownership

All operator-created resources except the API-key Secret and ConfigMap carry an `ownerReference` back to the `LLMModel` CR. Deleting the `LLMModel` garbage-collects them.

The API-key Secret and metadata ConfigMap deliberately omit `ownerReferences`. Their lifetime should outlive a re-apply of the `LLMModel` CR - an admin can recreate the CR without users losing their issued keys. Cleanup is handled by a finalizer on the `LLMModel` that removes both resources when the CR is being deleted.

---

## NetworkPolicy

Every `LLMModel` gets a NetworkPolicy that enforces default-deny ingress on the model pods, allowing traffic only from:

- **Envoy Gateway pods** (by namespace selector for `envoy-gateway-system`)
- **EPP pods** (by pod selector within the model namespace)
- **Prometheus** (by namespace selector or pod selector for monitoring)

This makes the Gateway the only path to the model, whether endpoints are enabled or not. Direct Service access from other in-cluster workloads is blocked. This NetworkPolicy is always created.

---

## Reconciliation flow

```
LLMModel CR applied
  |
  +-> Validate (webhook: subdomain DNS length, namespace label, same-as-operator-namespace, access)
  |
  +-> Phase: Pending
  |
  +-> Create PVC (if HF source + PVC storage type)
  +-> Create API key Secret + metadata ConfigMap in the LLMModel's own namespace
  +-> Create NetworkPolicy
  |
  +-> Create vLLM Deployment with init container for model download
  |
  +-> Phase: Downloading
  |     (init container downloading model; operator requeues to check pod phase)
  |
  +-> Init container completes, vLLM container starts
  |
  +-> Phase: Starting
  |     (wait for vLLM pods ready via /v1/models probe; requeue every 10s)
  |
  +-> Create InferencePool + EPP
  +-> Create AIGatewayRoutes (external + internal, if enabled)
  +-> Create SecurityPolicies (apiKeyAuth + JWT, targeting generated HTTPRoutes)
  |
  +-> Phase: Ready
```

The controller is event-driven and idempotent. Each reconciliation evaluates current state and takes the next appropriate action. When waiting for async operations (model download, pod startup), the controller requeues with a delay rather than blocking.

### Spec updates and restarts

When a running `LLMModel`'s spec changes, the operator updates the corresponding resources in place. For changes that affect the vLLM Deployment (image, `vllmArgs`, resources, replicas), the operator updates the Deployment spec and Kubernetes restarts the pods. For changes to access groups or endpoints, the operator updates SecurityPolicies and routes without touching the Deployment.

The Deployment's rollout strategy is controlled by `spec.serving.updateStrategy` (`Recreate`, the default, or `RollingUpdate`). The default is `Recreate` because model pods hold exclusive resources - the node's GPUs and a ReadWriteOnce model PVC - so on clusters without spare GPU capacity a rolling update's surged replacement pod can never schedule while the old pod is alive, deadlocking the rollout until the old ReplicaSet is deleted by hand. `Recreate` tears down the old pod first, trading brief downtime for a rollout that always completes. Clusters with enough free GPUs to run old and new pods side by side can set `RollingUpdate` for zero-downtime updates.

### /dev/shm for tensor parallelism

Every model pod mounts a memory-backed emptyDir at `/dev/shm` in the vLLM container. Kubernetes defaults `/dev/shm` to 64Mi, which is far too small for vLLM with `tensorParallelism > 1`: the worker processes exchange tensors through POSIX shared memory (and NCCL's SHM transport uses it too), so multi-GPU engines hang or crash during startup on the default size. The volume is capped at the model's memory limit when one is set; tmpfs pages count toward the container's memory limit regardless, so the cap reserves no extra memory.

Changes to `model.name`, `model.source`, `model.storage`, or `model.revision` require a new model download. The operator stores a hash of these fields as an annotation (`llm.nebari.dev/model-config-hash`) on the Deployment. When the hash changes, the operator deletes the existing Deployment and recreates it, re-entering the `Downloading` phase.

### Resource creation approach

The operator creates Kubernetes resources directly rather than rendering the upstream llm-d Helm charts. This gives clean ownership (ownerReferences), simpler reconciliation, and no Helm-in-operator complexity.

The tradeoff is tracking upstream changes manually. Each resource template in the operator code documents which llm-d-modelservice chart version it's based on. When llm-d releases updates, the team diffs their chart and updates the resource specs accordingly.

---

## Cluster-singleton reconcilers

Most operator work is per-`LLMModel` and flows through `LLMModelReconciler`. A small amount of state is cluster-wide and not owned by any single model: the shared-TLS Certificate covering `llm.<baseDomain>` and `llm-internal.<baseDomain>`, the cross-namespace `ReferenceGrant`s, and the HTTPS listeners on the external and internal Gateways.

These are reconciled by `ClusterTLSSingleton`, a `manager.Runnable` (not a controller-runtime Reconciler) that runs under leader election with a 5-minute resync after an initial reconcile on leader acquisition. It sets no `OwnerReferences` on its targets: the Certificate is cluster-scoped in spirit even though it lives in a namespace, and the Gateways are owned out-of-band by the platform - the operator only mutates their `.spec.listeners` slice in place, matched by listener name. On operator uninstall, the Certificate and listeners stay behind so in-flight traffic continues to terminate TLS while a new pack version rolls.

Use this pattern for any future cluster-wide concern; do not co-locate cluster-wide state inside `LLMModelReconciler`. The split keeps per-model reconciles fast and cluster-singleton reconciles rare and idempotent.

### Shared TLS and Gateway listeners

The singleton reconciler:

1. Ensures a `cert-manager.io/v1 Certificate` in the operator namespace named `nebari-llm-shared-tls`, with `dnsNames` set to the two shared hostnames and `issuerRef` pointing at the `ClusterIssuer` from `platform.tls.clusterIssuer`. In bring-your-own-certificate mode (`platform.tls.secretName` set), no Certificate is created - the operator instead expects a pre-provisioned `kubernetes.io/tls` Secret of that name in its namespace, deletes any Certificate it previously managed, and the listeners and ReferenceGrants below reference the user Secret. This is the path for air-gapped or private-CA clusters where ACME issuance is not possible; cert-manager is not required at all in this mode.
2. Ensures a `ReferenceGrant` in the operator namespace for each distinct Gateway namespace, permitting Gateways there to consume the shared Secret.
3. Patches HTTPS listeners named `llm-https` and `llm-internal-https` onto the external and internal Gateways. Pre-existing listeners for the base domain, Argo CD, Keycloak, or anything else on the shared Gateway are preserved; only the two operator-named listeners are managed.

Escape hatch: set `platform.gateway.manageSharedListeners: false`. The operator still creates the Certificate but skips the Gateway listener patch, leaving listener definition to whoever manages the shared Gateway out-of-band.

### Per-model routing

Every model on the cluster shares a single hostname pair: `llm.<baseDomain>` for the external endpoint and `llm-internal.<baseDomain>` for the internal endpoint. One TLS certificate covering both names serves every model.

Per-model routing happens via the `x-ai-eg-model` request header. The Envoy AI Gateway controller automatically extracts the `model` field from the JSON request body and surfaces it as that header. Each `LLMModel` produces an `AIGatewayRoute` whose single rule matches both the shared `Host` header and `x-ai-eg-model: <spec.model.name>` exactly. Clients set `model` in the request body, the same way they would against api.openai.com - no per-model URL is required.

The `endpoints.external.subdomain` field on `LLMModel` is currently unused at the routing layer. It is retained on the CRD for a future DNS-01 / wildcard cert path. See [Configuration](/configuration/) for field details.

---

## Dual endpoint auth

Each `LLMModel` gets two endpoints with different auth mechanisms. Access control is enforced at the gateway level by SecurityPolicies bound per-route: Keycloak JWT plus group authorization on the internal endpoint, and API-key authentication plus a per-model client-ID authorization allow-list on the external endpoint. (Models share one hostname and are dispatched by the `x-ai-eg-model` header, so isolation comes from the per-route policies, not from per-model hostnames.) A NetworkPolicy on model pods ensures all traffic flows through the Gateway.

### External endpoint

For CI pipelines, external applications, anything outside the cluster.

```
Client -> Authorization: Bearer sk-... -> Envoy AI Gateway -> apiKeyAuth SecurityPolicy -> InferencePool -> vLLM
```

- Hostname: `llm.<baseDomain>` (shared; per-model dispatch by `x-ai-eg-model` header)
- `AIGatewayRoute` for token counting, rate limiting, protocol normalization
- `SecurityPolicy` with `apiKeyAuth` attached to the generated `HTTPRoute` (same name as the `AIGatewayRoute`), per model
- `sanitize: true` strips the API key before forwarding to vLLM
- Per-model scoping is enforced by an `authorization` block on the external
  SecurityPolicy, not by authentication. API-key authentication is pooled across
  the shared listener (any valid key authenticates), so the SecurityPolicy adds
  `apiKeyAuth.forwardClientIDHeader: x-llm-client-id` and a deny-by-default
  `authorization` rule that allows only the client IDs present in that model's
  own api-keys Secret. A key minted for one model therefore returns 403 against
  any other model on the listener. The operator re-renders the allow-list when
  the key-manager mints or revokes a key, so a newly minted key activates within
  about a minute.
- `forwardClientIDHeader: x-llm-client-id` passes the authenticated client ID downstream for logging and GIE flow control
- API key Secret referenced from the SecurityPolicy without crossing namespace boundaries (Envoy Gateway's `apiKeyAuth` does not honor cross-namespace `credentialRefs`)

### Internal endpoint

For JupyterLab, in-cluster chat UIs, notebooks.

```
In-cluster app -> Authorization: Bearer <JWT> -> Envoy AI Gateway (internal) -> JWT SecurityPolicy -> InferencePool -> vLLM
```

- Hostname: `llm-internal.<baseDomain>` (shared; per-model dispatch by `x-ai-eg-model` header)
- `AIGatewayRoute` for token counting, rate limiting, protocol normalization
- `SecurityPolicy` with `jwt` auth attached to the generated `HTTPRoute`, per model
- JWT validation: verifies signature against the OIDC issuer's JWKS endpoint, checks audience, extracts groups from the configured claim, validates group membership against the model's `access.groups`
- No browser redirects - pure bearer token validation for service-to-service calls

**JWKS endpoint resolution note:** the operator currently constructs the JWKS URI as `<issuerURL>/protocol/openid-connect/certs`, the Keycloak convention. A non-Keycloak OIDC provider will not work out of the box even though the surrounding config fields are provider-agnostic. The long-term fix is to fetch the OIDC discovery document and read `jwks_uri`; until that lands, treat the internal SecurityPolicy JWKS path as Keycloak-only. Tracked in issue [#61](https://github.com/nebari-dev/llm-serving-pack/issues/61).

**JWT availability:** in Nebari, JupyterHub injects tokens into user pods. For other in-cluster services, the application must handle OIDC login and forward the resulting token. If a service cannot obtain a JWT, use the external endpoint with an API key instead.

### AIGatewayRoute and SecurityPolicy binding

Both endpoints use `AIGatewayRoute`. The `SecurityPolicy` targets the generated `HTTPRoute`, which has the same name as the `AIGatewayRoute` in the same namespace. This naming convention is used by Envoy AI Gateway's own documentation for `BackendTrafficPolicy` attachment.

The generated `HTTPRoute` is annotated with `gateway.envoyproxy.io/ai-gateway-generated: "true"` and carries an `ownerReference` back to the `AIGatewayRoute`.

If this naming convention changes in a future Envoy AI Gateway release, the operator can discover the `HTTPRoute` via `ownerReference` or the `ai-gateway-generated` annotation. The Helm chart pins the Envoy AI Gateway version; integration tests verify the naming convention holds.

### Auth configuration

Pack-level Helm values, not per-model (see [Configuration](/configuration/) for the full reference):

```yaml
auth:
  oidc:
    issuerURL: ""        # e.g., https://keycloak.nebari.example.com/realms/nebari
    groupsClaim: groups  # JWT claim containing group memberships
    audience: ""         # expected audience value for JWT validation
```

The operator combines these pack-level values with each `LLMModel`'s `access.groups` to generate per-model SecurityPolicies.

---

## PassthroughModel CRD

Issue [#95](https://github.com/nebari-dev/llm-serving-pack/issues/95). A PassthroughModel routes the shared `llm.<baseDomain>` / `llm-internal.<baseDomain>` endpoints to an external OpenAI-compatible provider (OpenRouter, api.openai.com, a remote vLLM) instead of a locally served model. The operator provisions gateway plumbing and the same two auth layers served models get; the key-manager lists PassthroughModels next to LLMModels so users mint API keys for external providers through the same UI.

```yaml
apiVersion: llm.nebari.dev/v1alpha1
kind: PassthroughModel
metadata:
  name: openrouter
  namespace: nebari-llm-serving-system
spec:
  provider:
    hostname: openrouter.ai          # bare hostname, TLS assumed
    port: 443                        # default
    schemaVersion: api/v1            # upstream path prefix (default v1)
    credentialSecretName: openrouter-api-key   # same-namespace Secret, key "apiKey"
  models:
    catchAll: true                   # any model id not claimed elsewhere
    declared:                        # explicit routes, advertised by /v1/models
      - openai/gpt-5.2
      - anthropic/claude-opus-4.6
  access:
    groups: [llm]                    # same semantics as LLMModel access
  endpoints:
    external: {enabled: true}
    internal: {enabled: true}
```

Generated resources (all in the CR's namespace, no serving stack):

| Resource | Purpose |
|---|---|
| Backend (`<name>-backend`) | Envoy Gateway fqdn backend for the provider |
| BackendTLSPolicy (`<name>-backend-tls`) | TLS validation toward the provider (System CAs, SNI) |
| AIServiceBackend (`<name>`) | OpenAI schema with the provider's path prefix |
| BackendSecurityPolicy (`<name>-upstream-auth`) | Injects the platform-owned provider API key upstream |
| AIGatewayRoute (`<name>-external` / `-internal`) | Declared-model rule (+`modelsOwnedBy`, external only) and/or Host-only catch-all rule |
| SecurityPolicy (`<name>-external-auth` / `-internal-auth`) | Same apiKeyAuth / JWT pair as served models |
| Secret (`<name>-api-keys`) + ConfigMap (`<name>-api-key-metadata`) | Per-provider user API keys, managed by the key-manager |

Semantics worth knowing:

- **Two auth layers.** Users authenticate to the cluster gateway (their own API key externally, Keycloak JWT internally); the gateway injects the platform's provider credential upstream. Users never see the provider key.
- **Served models win.** Per-LLMModel routes match Host AND `x-ai-eg-model` (two headers) while the catch-all matches Host only, so Gateway API precedence keeps any locally served model id local.
- **`modelsOwnedBy` is set on the external route only.** The gateway's `/v1/models` endpoint aggregates declared models across every route on the Gateway; declaring on both endpoints lists each model twice.
- **Catch-all conflicts are not validated.** Two catch-all PassthroughModels on the same gateway race for unclaimed model ids the same way two identical HTTPRoutes would; the webhook validates per-CR only, consistent with the LLMModel webhook dropping cross-CR collision checks. Document one catch-all per cluster as the supported shape.
- **Names are unique across both kinds.** Both an LLMModel and a PassthroughModel derive their api-keys Secret name as `<name>-api-keys`, so sharing a name would make the two controllers fight over one Secret. Both webhooks reject a CR whose name is already taken by the other kind in the same namespace. The key-manager cache also keeps kind-prefixed entries as defense in depth.
- **Status.** Phase is `Ready`/`Error` with conditions `BackendConfigured`, `ExternalEndpointReady`, `InternalEndpointReady`; missing AI Gateway CRDs surface as `ApplyFailed` conditions with a one-minute requeue rather than failing reconciliation outright. This surface-and-requeue handling is the intended operator-wide convention for a degraded gateway-apply. The older LLMModel reconciler instead logs-and-continues for the same case; converging it onto this convention is tracked as a follow-up.
- **Generated gateway resources are reconciled on-change-only.** Because the AI Gateway kinds (`Backend`, `BackendTLSPolicy`, `AIServiceBackend`, `BackendSecurityPolicy`, `AIGatewayRoute`, `SecurityPolicy`) are applied as unstructured objects, `SetupWithManager` registers only `For(&PassthroughModel{})` and does not `Owns(...)` them. If a generated resource is deleted out-of-band, it is not recreated until the next edit to the PassthroughModel CR.

---

## Key manager

A small web application behind `NebariApp` that lets authenticated users generate and manage API keys for models they can access.

### How it works

1. User hits the key manager UI at `llm-keys.<baseDomain>`
2. Keycloak/OIDC login via `NebariApp` auth
3. Key manager watches all `LLMModel` CRs, filters to models where `access.groups` overlaps with the user's OIDC groups (or `access.public: true`)
4. User sees only models they can access
5. User creates a key for a model; key manager generates `sk-<random>`, writes the client ID and key value to that model's `<name>-api-keys` Secret in the operator namespace, and writes metadata to the corresponding ConfigMap
6. Envoy Gateway's `apiKeyAuth` picks up the new Secret entry immediately

Revocation: remove the entry from the Secret and its corresponding metadata from the ConfigMap. Effect is immediate.

### Known limitation - group-change revocation not yet implemented

API keys are issued based on the user's groups at creation time. If a user later loses group access, existing keys continue to work - they are not tied to ongoing group membership.

Automatic revocation on group change is **planned but not implemented in v0.1**. A periodic audit loop is scaffolded (default interval: 5 minutes), but the OIDC userinfo lookup it depends on is a stub that always returns an error pending token exchange (`key-manager/cmd/main.go`), so the auditor skips revocation as a fail-safe. Setting `keyManager.oidcUserinfoURL` starts the loop but does not enable revocation - no key is ever revoked on group change today. A key keeps working until it is explicitly deleted. See [API key audit](#api-key-audit) below.

### Data model

No database. State is split across two Kubernetes resources per model, both in the operator namespace:

- **Secret** (`<model-name>-api-keys`): contains only the data Envoy Gateway needs. Each entry: key = client ID (e.g., `user-chuck-1`), value = the raw API key. This Secret is the source of truth for authentication. Individual Secrets are limited to 1 MiB, which supports roughly a few thousand keys per model (known scaling limit for v0.1).

- **ConfigMap** (`<model-name>-api-key-metadata`): contains a JSON blob per client ID with management metadata (creator username, creation timestamp, description). Separated from the Secret so the key manager can read and display metadata without exposing actual key values. Also limited to 1 MiB.

The key manager handles concurrent edits via optimistic concurrency (retry on 409 Conflict from the API server).

### API key Secret format

Envoy Gateway's `apiKeyAuth` expects Secret data entries where each key is the client ID and each value is the raw API key string. The key manager generates keys in the format `sk-<32-char-base64url-random>`. Example Secret data:

```yaml
data:
  user-chuck-1: c2stYWJjMTIzZGVmNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4  # base64 of "sk-abc123def..."
  user-alice-1: c2stZGVmNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4OTBhYmNk
```

### Key manager RBAC

The key manager runs with a dedicated ServiceAccount. Its RBAC covers two areas:

- **ClusterRole** for `LLMModel` read access: `get`, `list`, `watch` on `llmmodels.llm.nebari.dev` across all namespaces (broader than strictly needed today; a future follow-up will tighten this to a Role in the operator namespace)
- **Role in the operator namespace**: `get`, `list`, `create`, `update`, `patch`, `delete` on Secrets and ConfigMaps in the operator namespace only

### API key audit

The key manager has a scaffolded periodic background audit (configurable interval, default 5 minutes) intended to:

1. List all API key Secrets in the operator namespace
2. For each key entry, look up the creator's current groups via the OIDC userinfo endpoint
3. If the creator no longer belongs to a group that matches the model's `access.groups`, revoke the key

**This is not functional in v0.1.** The audit loop only starts when `keyManager.oidcUserinfoURL` is set (the default `values.yaml` ships it empty), but even then step 2 cannot complete: the userinfo lookup is a stub that always returns an error pending OIDC token exchange (`key-manager/cmd/main.go`), so the auditor skips revocation as a fail-safe. Setting the URL therefore does **not** enable group-change revocation - it remains planned work. No key is revoked on group change in the current release.

### NebariApp integration

```yaml
apiVersion: reconcilers.nebari.dev/v1
kind: NebariApp
metadata:
  name: nebari-llm-serving-key-manager
spec:
  hostname: llm-keys.<baseDomain>   # set via keyManager.nebariApp.hostname (no default)
  service:
    name: nebari-llm-serving-key-manager
    port: 8080
  routing:
    routes:
      - pathPrefix: /
  auth:
    enabled: true
    provider: keycloak               # or generic-oidc
    provisionClient: true
  gateway: public
```

---

## Envoy AI Gateway

Always used for both endpoints. The `envoyAIGateway.install` Helm value controls whether this pack installs the AI Gateway controller and CRDs, or assumes they are already present.

- `install: true` - the pack deploys the Envoy AI Gateway controller and CRDs
- `install: false` - the pack only creates `AIGatewayRoute` and `AIServiceBackend` resources; the operator checks for CRD existence at startup and logs a clear error if missing

Both external and internal endpoints use `AIGatewayRoute`, giving both paths token counting, rate limiting, and protocol normalization. Usage data is available for future cost tracking on both paths.

---

## Single-namespace deployment model

All pack components - the operator, the key manager, the `LLMModel` CRs, the model pods, the API-key Secrets, and the Envoy Gateway SecurityPolicies that reference them - live in **a single namespace per pack install**. The validating webhook rejects `LLMModel` CRs created in any other namespace.

This restriction exists because Envoy Gateway's `SecurityPolicy.spec.apiKeyAuth.credentialRefs` rejects cross-namespace Secret references and does not honor `ReferenceGrant` for that field. Co-locating the Secret with the SecurityPolicy is the only way to make `apiKeyAuth` work. The earlier multi-namespace design (one operator watching `llm-data-science`, `llm-engineering`, etc., with a dedicated `llm-api-keys` namespace bridged via `ReferenceGrant`) hit this wall and is no longer how the pack is laid out. See [#59](https://github.com/nebari-dev/llm-serving-pack/issues/59).

**Per-team isolation**: achieved by running multiple pack installs (one per team's operator namespace), not by a single operator watching multiple namespaces.

**Operator**: a single instance watches `LLMModel` CRs across all namespaces (current ClusterRole scope), but the webhook only accepts CRs in the operator's own namespace, so in practice it only ever reconciles CRs there.

**Key manager**: a single instance with RBAC scoped to the operator namespace for Secret/ConfigMap management.

**API-key Secrets**: live in the operator namespace alongside the SecurityPolicies that reference them. The `apiKeyAuth.credentialRefs` field carries no `namespace` field.

**Operator namespace setup**: the chart provisions the namespace and applies `nebari.dev/managed=true` (gated on `createNamespace: true`, default on). Set `createNamespace: false` if ArgoCD `managedNamespaceMetadata`, a Terraform module, or another tool is responsible for creating and labelling the namespace.

---

## Security model

**LLMModel creation**: any user with RBAC access to create `LLMModel` resources in a managed namespace can deploy a model. The `advanced` fields are explicitly typed and scoped to safe operations (scheduling constraints, env vars, extra CLI args). Security-sensitive pod spec fields (`securityContext`, `hostNetwork`, volumes, etc.) are not exposed.

**Network isolation**: model pods have a default-deny NetworkPolicy. Traffic is only allowed from the Envoy Gateway data plane, the EPP, and Prometheus. Direct Service access is blocked for all in-cluster workloads.

**Secret isolation**: API key Secrets live in the operator namespace with namespace-scoped RBAC. The key manager and the operator are the only components with access - the operator creates them, the key manager reads and updates them. SecurityPolicies in the same namespace reference them via `apiKeyAuth.credentialRefs` without crossing namespace boundaries.

**Gateway as security boundary**: all model access (external and internal) flows through Envoy Gateway, where auth is enforced via SecurityPolicy. The external endpoint uses `apiKeyAuth` with `sanitize: true` (API keys are stripped before reaching vLLM) plus a per-model authorization allow-list, so a key is accepted only by the model it was minted for. The internal endpoint uses JWT validation against the OIDC issuer's JWKS endpoint.
