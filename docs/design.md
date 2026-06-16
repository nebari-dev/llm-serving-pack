# Nebari LLM serving pack design

## Overview

A Nebari software pack for serving LLMs using llm-d with modelcar/OCI support for model distribution. The pack deploys a Go operator that watches a custom `LLMModel` CRD. Admins apply one LLMModel per model they want to serve. The operator handles everything downstream: model storage, vLLM serving pods, inference scheduling, routing, and access control.

An optional key manager service gives users a web UI to generate API keys for external access. Envoy AI Gateway provides token counting, rate limiting, and protocol normalization on both external and internal endpoints.

## Goals

- Admins deploy the pack once via ArgoCD, then declare models as LLMModel CRs
- Per-model access control via OIDC groups, generic for any OIDC provider, tested against Keycloak
- Two auth paths: API keys for external consumers, JWT bearer tokens for in-cluster users
- Model loading from HuggingFace (default) or OCI/modelcar images
- Intelligent inference scheduling via llm-d's InferencePool and EPP
- Token counting and rate limiting on both endpoints via Envoy AI Gateway
- NetworkPolicy enforcement: model pods are only reachable through the Gateway

## Non-goals for v0.1

- Prefill/decode disaggregation
- KV cache offloading
- Multi-accelerator support (AMD, Intel, etc.) - NVIDIA only initially
- Usage billing or cost chargeback
- Scale-to-zero autoscaling
- Per-key rate limiting or token quotas
- API key expiration (periodic audit revokes keys when group membership changes)
- Team-level shared API keys
- Kubernetes image volumes for OCI models (init-container copy is the baseline)

## LLMModel CRD

API group: `llm.nebari.dev/v1alpha1`, kind: `LLMModel`, namespace-scoped.

The namespace must have the `nebari.dev/managed=true` label (same requirement as NebariApp).

### Spec

```yaml
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: devstral-32b
  namespace: nebari-llm-serving-system
spec:
  model:
    name: "mistralai/Devstral-Small-2505"
    source: huggingface              # huggingface | oci
    revision: ""                     # optional: HF commit hash or tag for reproducible deployments
    authSecretName: hf-token         # K8s Secret with HF_TOKEN (huggingface only)
    # image: registry.example.com/models/devstral@sha256:abc123  # oci only, prefer digests
    storage:
      type: pvc                      # pvc | emptyDir
      size: 200Gi
      storageClassName: ""           # empty = cluster default
    preload: true                    # init container downloads model before vLLM starts

  resources:
    gpu:
      count: 2
      type: nvidia
    requests:
      cpu: "4"
      memory: "32Gi"
    limits:
      cpu: "8"
      memory: "64Gi"

  serving:
    image: ghcr.io/llm-d/llm-d-cuda:v0.6.0
    replicas: 1
    tensorParallelism: 2             # defaults to gpu.count
    dataParallelism: 1
    vllmArgs:
      - "--gpu-memory-utilization=0.95"
      - "--max-model-len=32768"
    monitoring:
      enabled: true

  access:
    public: false                    # if true, all authenticated users can access (ignores groups)
    groups:                          # OIDC groups that can access this model
      - data-science

  endpoints:
    external:
      enabled: true
      subdomain: ""                  # reserved for future per-model FQDN routing; currently unused at the routing layer
    internal:
      enabled: true

  advanced:
    vllm:
      extraArgs: []                  # additional vLLM CLI args beyond vllmArgs
      extraEnv: []                   # additional env vars (corev1.EnvVar)
      tolerations: []                # node scheduling
      nodeSelector: {}               # node scheduling
      affinity: {}                   # pod affinity/anti-affinity
    inferencePool:
      schedulerConfig: {}            # EPP plugin configuration
```

### Advanced overrides

The `advanced` section provides typed fields for configuration not surfaced as first-class CRD fields. Unlike the previous raw JSON override design, these fields are explicitly typed to prevent security escalation. Users cannot inject arbitrary pod spec fields (securityContext, hostNetwork, volumes, etc.).

Available fields:
- `vllm.extraArgs`: additional CLI arguments passed to vLLM (appended after `serving.vllmArgs`)
- `vllm.extraEnv`: additional environment variables on the vLLM container
- `vllm.tolerations`: node tolerations for GPU node scheduling
- `vllm.nodeSelector`: node selector for targeting specific node pools
- `vllm.affinity`: pod affinity/anti-affinity rules
- `inferencePool.schedulerConfig`: EPP scheduler plugin configuration

### Status

```yaml
status:
  phase: Ready                       # Pending | Downloading | Starting | Ready | Degraded | Error
  conditions:
    - type: ModelDownloaded
      status: "True"                 # True | False | Unknown
      reason: DownloadComplete       # DownloadComplete | DownloadInProgress | DownloadFailed
      lastTransitionTime: "2026-03-25T12:00:00Z"
    - type: ServingReady
      status: "True"
      reason: AllReplicasReady       # AllReplicasReady | PartialReplicasReady | NoReplicasReady
    - type: ExternalEndpointReady
      status: "True"
      reason: RouteConfigured        # RouteConfigured | RouteNotConfigured | EndpointDisabled
    - type: InternalEndpointReady
      status: "True"
      reason: RouteConfigured
    - type: AccessControlConfigured
      status: "True"
      reason: SecurityPoliciesApplied  # SecurityPoliciesApplied | PolicyCreationFailed
  observedGeneration: 1
  modelSize: "63.2 GB"
  replicas:
    ready: 1
    desired: 1
  endpoints:
    external: "https://llm.nebari.example.com"
    internal: "https://llm-internal.nebari.example.com"
```

Phase semantics:
- **Pending**: CR accepted, operator starting reconciliation
- **Downloading**: model download in progress (HF source only)
- **Starting**: model downloaded, vLLM pods starting up
- **Ready**: all replicas ready, endpoints configured
- **Degraded**: some but not all replicas ready; model still serving traffic
- **Error**: reconciliation failed, check conditions for details

When both endpoints are disabled, the operator still creates the vLLM Deployment and InferencePool but skips route and SecurityPolicy creation. The model is deployed but not exposed - the NetworkPolicy blocks all direct access, so the model can only be verified via `kubectl get llmmodel` status and pod logs. This is useful for testing that a model launches correctly before exposing it.

### Hostnames and per-model routing

Every model on the cluster shares a single hostname pair: `llm.<baseDomain>` for the external endpoint and `llm-internal.<baseDomain>` for the internal endpoint, where `baseDomain` comes from pack-level Helm values. One TLS certificate covering both names serves every model. This is required for HTTP-01 issuance, where wildcard SANs are not available - a per-model FQDN scheme would force a new SAN for every new model.

Per-model routing happens via the `x-ai-eg-model` request header. The Envoy AI Gateway controller automatically extracts the `model` field from the JSON request body of OpenAI-compatible requests and surfaces it as that header. Each LLMModel produces an AIGatewayRoute whose single rule matches both the shared `Host` header (`llm.<baseDomain>` external, `llm-internal.<baseDomain>` internal) AND `x-ai-eg-model: <spec.model.name>` exactly. The Host match scopes the generated HTTPRoute to the shared listener so unrelated listeners on the same Gateway (the key-manager UI, Argo CD, Keycloak, the base domain) are not caught by per-model SecurityPolicies. The `x-ai-eg-model` match handles the per-model dispatch within the shared listener. Clients still do not need to know any per-model URL - they just set `model` in the request body, the same way they would against api.openai.com.

The `endpoints.external.subdomain` field on LLMModel is currently unused at the routing layer. It is retained on the CRD and validated by the webhook for the >63-character DNS label limit so it can be re-wired into routing if and when wildcard certs (DNS-01) become available across deployments. There is no longer a cross-model subdomain collision check; the webhook does not list every LLMModel on admission.

This supersedes the per-model FQDN + `Host`-header design from issue [#64](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/64) once cluster validation showed the TLS SAN cost was prohibitive under HTTP-01.

#### Who owns the certificate and the Gateway listeners

The operator owns both, as a cluster-singleton. At startup (and every 5 minutes after, as a backstop) the operator:

1. Ensures a `cert-manager.io/v1 Certificate` in its own namespace named `nebari-llm-shared-tls`, with `dnsNames` set to the two shared hostnames and `issuerRef` pointing at the `ClusterIssuer` from `platform.tls.clusterIssuer` (default `letsencrypt-production`). cert-manager writes the issued cert into a Secret of the same name.
2. Ensures a `gateway.networking.k8s.io/v1beta1 ReferenceGrant` in its own namespace for each distinct Gateway namespace, permitting Gateways there to consume the shared Secret.
3. Patches HTTPS listeners named `llm-https` / `llm-internal-https` onto the external and internal Gateways, with `tls.certificateRefs` pointing at the shared Secret. The merge is keyed on listener name: pre-existing listeners for the base domain, Argo CD, Keycloak, or anything else on the shared Gateway are preserved; only the two operator-named listeners are managed.

This places the operator consistent with the nebari-operator NebariApp pattern - the thing that knows it needs TLS is the thing that provisions TLS. The chart does not template a Certificate, and the runbook does not ask admins to add listeners by hand.

Escape hatch: set `platform.gateway.manageSharedListeners: false` in chart values. The operator still creates the Certificate (so admins get the rendered Secret for free), but skips the Gateway listener patch, leaving the listener definition to whoever owns the shared Gateway out-of-band.

A validating webhook still rejects LLMModels if:
- The effective subdomain exceeds the 63-character DNS label limit
- The namespace lacks the `nebari.dev/managed=true` label
- The namespace differs from the operator's own namespace
- `spec.access` declares neither `public=true` nor any groups

### Model revision pinning

For HuggingFace models, `model.revision` pins a specific commit hash or tag (e.g., `main`, `v1.0`, `abc123def`). When empty, the init container downloads the latest version. When set, it passes `--revision <value>` to the HuggingFace download. This makes deployments reproducible - the same LLMModel spec always produces the same model weights.

For OCI sources, use image digests (`@sha256:...`) for true reproducibility. Image tags can move; digests are immutable.

Changes to `model.revision` are treated as breaking changes that trigger a re-download.

## Architecture

### Components deployed by the Helm chart (pack install)

1. **LLMModel CRD** definition
2. **LLM operator** - Go controller (kubebuilder/controller-runtime) watching LLMModel CRs in its own namespace
3. **Key manager** (conditional, `keyManager.enabled`) - web UI + REST API behind a NebariApp with Keycloak/OIDC auth
4. **Envoy AI Gateway** (conditional, `envoyAIGateway.install`) - controller and CRDs; when false, assumes pre-installed

The chart creates the operator namespace and labels it `nebari.dev/managed=true` (gated on `createNamespace: true`, default on). Per [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59) all per-model resources - including API-key Secrets - live in this same namespace.

### Resources created by the operator per LLMModel

All resources live in the LLMModel's own namespace, which (per the validating webhook) is the operator's namespace.

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
| SecurityPolicy (external) | apiKeyAuth referencing the per-model Secret in the same namespace |
| SecurityPolicy (internal) | JWT validation with group claim matching against access.groups |
| Secret (API keys) | Per-model API key store (created by operator, data managed by key manager) |
| ConfigMap (key metadata) | Per-model metadata for API keys (creator, timestamp, description) |
| PodMonitor | Prometheus metrics scraping (when monitoring enabled) |

### Resource ownership

All operator-created resources except the API-key Secret + ConfigMap have an ownerReference back to the LLMModel CR. Deleting the LLMModel garbage-collects them.

The API-key Secret and metadata ConfigMap deliberately omit ownerReferences. Their lifetime should outlive a reapply of the LLMModel CR (so an admin can recreate the LLMModel without users losing their issued keys). Cleanup is handled by a finalizer on the LLMModel that removes both resources when the CR is being deleted.

### NetworkPolicy

Every LLMModel gets a NetworkPolicy that enforces default-deny ingress on the model pods, allowing traffic only from:

- **Envoy Gateway pods** (by namespace selector for `envoy-gateway-system`)
- **EPP pods** (by pod selector within the model namespace)
- **Prometheus** (by namespace selector or pod selector for monitoring)

This makes the Gateway the only path to the model, whether endpoints are enabled or not. Direct Service access from other in-cluster workloads is blocked. This is not optional - the NetworkPolicy is always created.

### Key manager RBAC

The key manager runs with a dedicated ServiceAccount. Its RBAC is scoped to two areas:

- **ClusterRole** for LLMModel read access: `get`, `list`, `watch` on `llmmodels.llm.nebari.dev` across all namespaces (broader than strictly needed today; see #59 follow-ups for tightening to a Role in the operator namespace)
- **Role in the operator namespace**: `get`, `list`, `create`, `update`, `patch`, `delete` on Secrets and ConfigMaps in the operator namespace only

The key manager can only access Secrets in the operator namespace, not anywhere else. Per [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59) the API-key Secrets live in this same namespace, so this Role grants exactly the access the key manager needs and nothing more.

### API key audit

The key manager runs a periodic background audit (configurable interval, default 5 minutes) that:

1. Lists all API key Secrets in the operator namespace
2. For each key entry, looks up the creator's current groups via the OIDC userinfo endpoint
3. If the creator no longer belongs to a group that matches the model's `access.groups`, revokes the key

There is a window (up to the audit interval) where a user who has lost group access can still use existing keys. This is acceptable for v0.1 and consistent with how most RBAC systems handle eventual consistency.

### Resource creation approach

The operator creates Kubernetes resources directly rather than rendering the upstream llm-d Helm charts. This gives us clean ownership (ownerReferences), simpler reconciliation, and no Helm-in-operator complexity.

The tradeoff is tracking upstream changes manually. Each resource template in the operator code must document which llm-d-modelservice chart version it's based on. When llm-d releases updates, we diff their chart and update our resource specs.

### Cluster-singleton reconcilers

Most operator work is per-LLMModel and flows through `LLMModelReconciler`. A small amount of state is cluster-wide and not owned by any single LLMModel: the shared-TLS Certificate covering `llm.<baseDomain>` and `llm-internal.<baseDomain>`, the cross-namespace ReferenceGrants, and the HTTPS listeners on the external and internal Gateways.

These are reconciled by `ClusterTLSSingleton`, a `manager.Runnable` (not a controller-runtime Reconciler) that runs under leader election with a 5-minute resync after an initial reconcile on leader acquisition. It sets no OwnerReferences on its targets: the Certificate is cluster-scoped in spirit even though it lives in a namespace, and the Gateways are owned out-of-band by the platform - the operator only mutates their `.spec.listeners` slice in place, matched by listener name. On operator uninstall the Certificate and listeners stay behind so in-flight traffic continues to terminate TLS while a new pack version rolls.

Use this pattern for any future cluster-wide concern; do not co-locate cluster-wide state inside `LLMModelReconciler`. The split keeps per-model reconciles fast and keeps cluster-singleton reconciles rare and idempotent.

### Reconciliation flow

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

### Spec updates and rolling restarts

When a running LLMModel's spec changes, the operator updates the corresponding resources in place. For changes that affect the vLLM Deployment (image, vllmArgs, resources, replicas), the operator updates the Deployment spec and Kubernetes handles the rolling restart. For changes to access groups or endpoints, the operator updates SecurityPolicies and routes without touching the Deployment.

Changes to `model.name`, `model.source`, `model.storage`, or `model.revision` require a new model download. The operator stores a hash of these fields as an annotation (`llm.nebari.dev/model-config-hash`) on the Deployment. When the hash changes, the operator deletes the existing Deployment and recreates it, re-entering the Downloading phase.

## Dual endpoint auth

Each LLMModel gets two endpoints with different auth mechanisms. Per-model hostnames ensure access control is enforced at the gateway level without request body inspection. A NetworkPolicy on model pods ensures all traffic flows through the Gateway.

### External endpoint

For CI pipelines, external applications, anything outside the cluster.

```
Client -> Authorization: Bearer sk-... -> Envoy AI Gateway -> apiKeyAuth SecurityPolicy -> InferencePool -> vLLM
```

- Hostname: `llm.<baseDomain>` (shared across all models on the cluster; per-model dispatch is by `x-ai-eg-model` header)
- AIGatewayRoute for token counting, rate limiting, protocol normalization
- SecurityPolicy with `apiKeyAuth` attached to the generated HTTPRoute (same name as AIGatewayRoute), per model
- `sanitize: true` strips the API key before forwarding to vLLM
- `forwardClientIDHeader: X-Client-ID` passes the authenticated client ID downstream for logging and GIE flow control
- API key Secret referenced same-namespace from the SecurityPolicy (per [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59) - Envoy Gateway's APIKeyAuth does not honor cross-namespace credentialRefs)

### Internal endpoint

For JupyterLab, in-cluster chat UIs, notebooks.

```
In-cluster app -> Authorization: Bearer <JWT> -> Envoy AI Gateway (internal) -> JWT SecurityPolicy -> InferencePool -> vLLM
```

- Hostname: `llm-internal.<baseDomain>` (shared across all models; per-model dispatch is by `x-ai-eg-model` header)
- AIGatewayRoute for token counting, rate limiting, protocol normalization
- SecurityPolicy with `jwt` auth (not OIDC) attached to the generated HTTPRoute, per model
- JWT validation: verifies signature against the OIDC issuer's JWKS endpoint, checks audience, extracts groups from the configured claim, validates group membership against the model's `access.groups`
- No browser redirects - this is pure bearer token validation for service-to-service calls

JWKS endpoint resolution: the operator currently constructs the JWKS URI as `<issuerURL>/protocol/openid-connect/certs`, the Keycloak convention. This matches the rest of the pack's Keycloak assumptions (issuer URL format, group-membership mapper) but means a non-Keycloak OIDC provider will not work out of the box even though the surrounding config fields are provider-agnostic. The long-term fix is to fetch `<issuerURL>/.well-known/openid-configuration` and read `jwks_uri` from the discovery document; until that lands, treat the internal SecurityPolicy JWKS path as Keycloak-only. Tracked in issue #61.

The `jwt` SecurityPolicy type in Envoy Gateway validates bearer tokens without OIDC redirect flows. It takes the issuer URL, fetches the JWKS, and validates the token signature, expiry, and claims. This is the correct mechanism for service-to-service auth where the calling service already has a JWT.

Note on JWT availability: in Nebari, JupyterHub injects tokens into user pods. For other in-cluster services, the application must handle OIDC login and forward the resulting token. If a service cannot obtain a JWT, it should use the external endpoint with an API key instead.

### AIGatewayRoute and SecurityPolicy binding

Both endpoints use AIGatewayRoute (for token counting and rate limiting). SecurityPolicy targets the generated HTTPRoute, which has the same name as the AIGatewayRoute in the same namespace. This naming convention is used by Envoy AI Gateway's own documentation for BackendTrafficPolicy attachment.

The generated HTTPRoute is annotated with `gateway.envoyproxy.io/ai-gateway-generated: "true"` and has an ownerReference back to the AIGatewayRoute.

Mitigations for the fact that this naming convention is documented as "an implementation detail subject to change":
- The Helm chart pins the Envoy AI Gateway version
- Integration tests verify the generated HTTPRoute name matches the AIGatewayRoute name
- If the convention changes in a future release, the operator can discover the HTTPRoute via ownerReference or the `ai-gateway-generated` annotation

### Auth configuration

Pack-level Helm values, not per-model:

```yaml
auth:
  oidc:
    issuerURL: ""                    # e.g., https://keycloak.nebari.example.com/realms/nebari
    groupsClaim: groups              # JWT claim containing group memberships
    audience: ""                     # expected audience value for JWT validation
```

The operator combines these pack-level values with each LLMModel's `access.groups` to generate per-model SecurityPolicies. This is generic for any OIDC provider but tested against Keycloak.

## PassthroughModel CRD

Issue [#95](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/95). A
PassthroughModel routes the shared `llm.<baseDomain>` / `llm-internal.<baseDomain>`
endpoints to an external OpenAI-compatible provider (OpenRouter, api.openai.com,
a remote vLLM) instead of a locally served model. The operator provisions
gateway plumbing and the same two auth layers served models get; the key-manager
lists PassthroughModels next to LLMModels so users mint API keys for external
providers through the same UI.

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

- **Two auth layers.** Users authenticate to the cluster gateway (their own
  API key externally, Keycloak JWT internally); the gateway injects the
  platform's provider credential upstream. Users never see the provider key.
- **Served models win.** Per-LLMModel routes match Host AND `x-ai-eg-model`
  (two headers) while the catch-all matches Host only, so Gateway API
  precedence keeps any locally served model id local.
- **`modelsOwnedBy` is set on the external route only.** The gateway's
  `/v1/models` endpoint aggregates declared models across every route on the
  Gateway; declaring on both endpoints lists each model twice.
- **Catch-all conflicts are not validated.** Two catch-all PassthroughModels
  on the same gateway race for unclaimed model ids the same way two identical
  HTTPRoutes would; the webhook validates per-CR only, consistent with the
  LLMModel webhook dropping cross-CR collision checks. Document one catch-all
  per cluster as the supported shape.
- **Names are unique across both kinds.** Both an LLMModel and a
  PassthroughModel derive their api-keys Secret name as `<name>-api-keys`, so
  sharing a name would make the two controllers fight over one Secret. Both
  webhooks reject a CR whose name is already taken by the other kind in the
  same namespace. The key-manager cache additionally keeps kind-prefixed
  entries as defense in depth.
- **Status.** Phase is Ready/Error with conditions `BackendConfigured`,
  `ExternalEndpointReady`, `InternalEndpointReady`; missing AI Gateway CRDs
  surface as `ApplyFailed` conditions with a one-minute requeue rather than
  failing reconciliation outright.

## Key manager

A small web application behind NebariApp that lets authenticated users generate and manage API keys for models they can access.

### How it works

1. User hits the key manager UI at `keys.llm.<baseDomain>`
2. Keycloak/OIDC login via NebariApp auth
3. Key manager watches all LLMModel CRs, filters to models where `access.groups` overlaps with the user's OIDC groups (or `access.public: true`)
4. User sees only models they can access
5. User creates a key for a model; key manager generates `sk-<random>`, writes the client ID and key value to that model's `<name>-api-keys` Secret in the operator namespace, and writes metadata to the corresponding ConfigMap
6. Envoy Gateway's apiKeyAuth picks up the new Secret entry immediately

Revocation: remove the entry from the Secret and its corresponding metadata from the ConfigMap. Immediate effect.

### Known limitation: keys are not continuously tied to group membership

API keys are issued based on the user's groups at creation time. If a user later loses group access, existing keys continue to work until the periodic audit revokes them (default: 5 minute interval). This is stated plainly because it is not the same as ongoing group-based authorization. For v0.1, this eventual consistency is acceptable.

### Data model

No database. State is split across two Kubernetes resources per model, both in the operator namespace (alongside the LLMModel CR and the SecurityPolicy that reads the Secret):

- **Secret** (`<model-name>-api-keys`): contains only the data Envoy Gateway needs. Each entry: key = client ID (e.g., `user-chuck-1`), value = the API key. This Secret is the source of truth for authentication because Envoy Gateway's apiKeyAuth reads from it natively. Individual Secrets are limited to 1 MiB, which supports roughly a few thousand keys per model. This is a known scaling limit for v0.1.

- **ConfigMap** (`<model-name>-api-key-metadata`): contains a JSON blob per client ID with management metadata (creator username, creation timestamp, description). Separated from the Secret so the key manager can read/display metadata without exposing the actual key values. Also limited to 1 MiB.

The key manager handles concurrent edits via optimistic concurrency (retry on 409 Conflict from the API server).

### API key Secret format

Envoy Gateway's apiKeyAuth expects Secret data entries where each key is the client ID and each value is the raw API key string. The key manager generates keys in the format `sk-<32-char-base64url-random>`. Example Secret data:

```yaml
data:
  user-chuck-1: c2stYWJjMTIzZGVmNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4  # base64 of "sk-abc123def..."
  user-alice-1: c2stZGVmNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4OTBhYmNk
```

### NebariApp

```yaml
apiVersion: reconcilers.nebari.dev/v1
kind: NebariApp
metadata:
  name: llm-key-manager
spec:
  hostname: keys.llm.<baseDomain>
  service:
    name: llm-key-manager
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

## Envoy AI Gateway

Always used for both endpoints. The `envoyAIGateway.install` Helm value controls whether this pack installs the AI Gateway controller and CRDs, or assumes they're already present.

When `install: true`: pack deploys the Envoy AI Gateway controller and CRDs.
When `install: false`: pack only creates AIGatewayRoute and AIServiceBackend resources. Operator checks for CRD existence at startup and logs a clear error if missing.

Both external and internal endpoints use AIGatewayRoute, giving both paths token counting, rate limiting, and protocol normalization. This means usage data is available for future cost tracking on both paths.

## Model loading

Two supported sources, selectable per LLMModel.

### HuggingFace (default)

The operator creates a PVC (when `storage.type: pvc`) and a Deployment with an init container that downloads the model. The init container uses a file-based lock to coordinate concurrent downloads when multiple replicas share the same PVC.

**Lock mechanism for concurrent replicas:**

The init container uses a `.locked` file on the PVC to prevent redundant concurrent downloads:

1. Try to acquire lock via `noclobber` shell option (atomic create, fails if file exists)
2. If lock acquired: run `huggingface-cli download` (idempotent, checksums existing files), then release lock
3. If lock held by another pod: check timestamp in lock file, wait and retry. If lock is older than 1 hour, treat as stale and take over
4. `huggingface-cli download` is idempotent: it checksums existing files and only downloads what's missing. If a pod dies mid-download, the next pod resumes where it left off after the lock times out

No `.complete` marker file. The init container always runs `huggingface-cli download`, which verifies file integrity via checksums. This handles both fresh downloads and recovery from partial downloads.

PVC storage means the model survives pod restarts and rescheduling without re-downloading. emptyDir is available for small models where persistence isn't needed.

Note: when `storage.type: emptyDir` and `preload: true`, every pod restart triggers a full re-download. The validating webhook emits a warning (not a rejection) for this combination.

### OCI/modelcar

The baseline OCI approach uses an init container to copy model files from the OCI image to a shared volume. The init container uses the `model.image` as its container image and copies files to a shared emptyDir:

```
Init container (image: model.image) -> cp -r /models/. /shared-models/
vLLM container -> reads from /model-cache (same emptyDir)
```

Kubernetes image volumes (alpha in 1.31, beta in 1.35) are a future optimization that would eliminate the copy step. For v0.1, the init-container path is the only supported OCI mechanism, since many clusters do not have the ImageVolume feature gate enabled.

For reproducibility, use image digests (`@sha256:...`) rather than tags. Tags can move; digests are immutable.

## Pack-level Helm values

```yaml
platform:
  baseDomain: nebari.example.com
  gateway:
    external:
      name: nebari-gateway
      namespace: envoy-gateway-system
    internal:
      name: nebari-internal-gateway
      namespace: envoy-gateway-system
    manageSharedListeners: true
  tls:
    clusterIssuer: letsencrypt-production

auth:
  oidc:
    issuerURL: ""
    groupsClaim: groups
    audience: ""

envoyAIGateway:
  install: false   # not yet implemented (#44); install via a separate ArgoCD app

keyManager:
  enabled: true
  image:
    repository: ghcr.io/nebari-dev/nebari-llm-serving-pack/key-manager
    # tag defaults to .Chart.AppVersion when empty; override only for
    # testing specific image builds (e.g. tag: sha-abc1234)
    tag: ""
  auditInterval: 5m

operator:
  image:
    repository: ghcr.io/nebari-dev/nebari-llm-serving-pack/operator
    tag: ""

defaults:
  serving:
    image: ghcr.io/llm-d/llm-d-cuda:v0.6.0
  resources:
    requests:
      cpu: "4"
      memory: "32Gi"
    limits:
      cpu: "8"
      memory: "64Gi"
  monitoring:
    enabled: true
```

These defaults apply to all LLMModels unless overridden in the individual CR spec. All images use pinned version tags, not `:latest`.

## Deployment flow

1. Admin creates an ArgoCD Application pointing to the chart:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: nebari-llm-serving
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/nebari-dev/nebari-llm-serving-pack
    path: charts/nebari-llm-serving
    targetRevision: v0.1.0-alpha.7   # pin a real release; image tags follow
    helm:
      valueFiles:
        - values-prod.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: nebari-llm-serving-system
```

2. ArgoCD deploys the pack: CRD, operator, key manager, AI Gateway. The chart also creates the operator namespace and labels it `nebari.dev/managed=true` (gated on `createNamespace: true`, default on).
3. Admin applies LLMModel CRs in the operator namespace (directly or via a second ArgoCD Application for GitOps). Per [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59) the validating webhook rejects LLMModels created anywhere else.
4. Operator reconciles each LLMModel through its phases
5. Users access the key manager UI to generate API keys, or use their JWT for in-cluster access

## Example: two models with different access

```yaml
# Model available to all authenticated users
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: devstral-32b
  namespace: nebari-llm-serving-system
spec:
  model:
    name: "mistralai/Devstral-Small-2505"
    source: huggingface
    authSecretName: hf-token
    storage:
      type: pvc
      size: 80Gi
  resources:
    gpu:
      count: 1
      type: nvidia
  serving:
    replicas: 2
    tensorParallelism: 1
    monitoring:
      enabled: true
    vllmArgs:
      - "--gpu-memory-utilization=0.95"
  access:
    public: true
  endpoints:
    external:
      enabled: true
      subdomain: devstral
    internal:
      enabled: true
---
# Expensive model, data science team only
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: qwen3-235b
  namespace: nebari-llm-serving-system
spec:
  model:
    name: "Qwen/Qwen3-235B-A22B"
    source: huggingface
    authSecretName: hf-token
    storage:
      type: pvc
      size: 500Gi
  resources:
    gpu:
      count: 4
      type: nvidia
    requests:
      cpu: "8"
      memory: "64Gi"
    limits:
      cpu: "16"
      memory: "128Gi"
  serving:
    replicas: 1
    tensorParallelism: 4
    monitoring:
      enabled: true
    vllmArgs:
      - "--gpu-memory-utilization=0.95"
      - "--max-model-len=16384"
  access:
    groups:
      - data-science
  endpoints:
    external:
      enabled: true
      subdomain: qwen
    internal:
      enabled: true
```

After applying these, a user in the `data-science` group can access both models. A user without the `data-science` group can only access Devstral (because it has `public: true`). The key manager UI shows each user only the models they're allowed to use.

## Repository structure

```
nebari-llm-serving-pack/
  charts/
    nebari-llm-serving/
      Chart.yaml
      values.yaml
      crds/
        llmmodel-crd.yaml
      templates/
        operator-deployment.yaml
        operator-rbac.yaml
        operator-serviceaccount.yaml
        nebariapp-key-manager.yaml
        key-manager-deployment.yaml
        key-manager-service.yaml
        key-manager-rbac.yaml
        api-keys-namespace.yaml
        envoy-ai-gateway/
          ...
  operator/
    cmd/
      main.go
    internal/
      controller/
        llmmodel_controller.go
        resources/
          modelservice.go          # based on llm-d-modelservice chart v0.4.7
          inferencepool.go         # based on GIE inferencepool chart v1.4.0
          routing.go
          auth.go
          storage.go
          networkpolicy.go
      webhook/
        validation.go
      config/
        config.go
    api/
      v1alpha1/
        llmmodel_types.go
        zz_generated.deepcopy.go
  key-manager/
    cmd/
      main.go
    internal/
      api/
      ui/
      secrets/
      audit/                       # periodic key audit
  .github/
    workflows/
      add-to-project.yaml
      lint.yaml
      test.yaml
      test-integration.yaml
      build-images.yaml
      release.yaml
  dev/
    Makefile
  docs/
```

## Single-namespace deployment model

Per [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59), all pack components - the operator, the key manager, the LLMModel CRs, the model pods, the API-key Secrets, and the Envoy Gateway SecurityPolicies that reference them - live in **a single namespace per pack install**. The validating webhook on LLMModel rejects CRs created in any other namespace.

This restriction exists because Envoy Gateway's `SecurityPolicy.spec.apiKeyAuth.credentialRefs` rejects cross-namespace Secret references and does not honor `ReferenceGrant` for that field. Co-locating the Secret with the SecurityPolicy is the only way to make `apiKeyAuth` work today. The earlier multi-namespace design (one operator watching `llm-data-science`, `llm-engineering`, etc., with a dedicated `llm-api-keys` namespace bridged via ReferenceGrant) hit this wall and is no longer how the pack is laid out.

**Operator**: single instance deployed by the Helm chart. Watches LLMModel CRs across all namespaces (current ClusterRole scope) but the webhook only accepts CRs in the operator's own namespace, so in practice it only ever reconciles CRs there. Tightening the ClusterRole to a Role on the operator namespace is a follow-up; see #59 discussion.

**Key manager**: single instance, RBAC scoped to the operator namespace for Secret/ConfigMap management. The Secrets it manages live in the same namespace it does, so a same-namespace Role is sufficient.

**API-key Secrets**: live in the operator namespace alongside the SecurityPolicies that reference them. The `apiKeyAuth.credentialRefs` field on each model SecurityPolicy carries no `namespace` field (an explicit assertion in `auth_test.go`).

**Per-team isolation**: achieved by running multiple pack installs (one per team's operator namespace), not by a single operator watching multiple namespaces. This is operationally heavier than the original design but matches the only path the upstream auth machinery supports.

**Operator namespace setup**: the chart provisions the operator namespace and applies `nebari.dev/managed=true` (gated on `createNamespace: true`, default on). Set `createNamespace: false` if something else - ArgoCD `managedNamespaceMetadata`, a Terraform module - is responsible for creating and labelling the namespace.

**POD_NAMESPACE**: the operator deployment injects `POD_NAMESPACE` from the downward API and passes it to the webhook setup. Empty `POD_NAMESPACE` puts the webhook in test mode and skips the same-namespace check (used by envtest, which doesn't run the operator inside a pod).

## Security model

**LLMModel creation**: any user with RBAC access to create LLMModel resources in a managed namespace can deploy a model. The `advanced` fields are explicitly typed and scoped to safe operations (scheduling constraints, env vars, extra CLI args). Security-sensitive pod spec fields (securityContext, hostNetwork, volumes, etc.) are not exposed.

**Network isolation**: model pods have a default-deny NetworkPolicy. Traffic is only allowed from the Envoy Gateway data plane, the EPP, and Prometheus. Direct Service access is blocked for all in-cluster workloads.

**Secret isolation**: API key Secrets live in the operator namespace with namespace-scoped RBAC. The key manager and the operator are the only components with access to these Secrets - the operator creates them, the key manager reads/updates them. SecurityPolicies in the same namespace reference them via `apiKeyAuth.credentialRefs` without crossing namespace boundaries (which Envoy Gateway does not allow for that field; see [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59)).

**Gateway as security boundary**: all model access (external and internal) flows through Envoy Gateway, where auth is enforced via SecurityPolicy. The external endpoint uses apiKeyAuth with `sanitize: true` (API keys are stripped before reaching vLLM). The internal endpoint uses JWT validation against the OIDC issuer's JWKS endpoint.

## Open questions

None remaining. All design decisions have been made.
