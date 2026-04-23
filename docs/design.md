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
  namespace: llm-serving
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
      subdomain: ""                  # optional override; defaults to slugified metadata.name
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
    external: "https://devstral-32b.llm.nebari.example.com"
    internal: "https://devstral-32b.llm-internal.nebari.example.com"
```

Phase semantics:
- **Pending**: CR accepted, operator starting reconciliation
- **Downloading**: model download in progress (HF source only)
- **Starting**: model downloaded, vLLM pods starting up
- **Ready**: all replicas ready, endpoints configured
- **Degraded**: some but not all replicas ready; model still serving traffic
- **Error**: reconciliation failed, check conditions for details

When both endpoints are disabled, the operator still creates the vLLM Deployment and InferencePool but skips route and SecurityPolicy creation. The model is deployed but not exposed - the NetworkPolicy blocks all direct access, so the model can only be verified via `kubectl get llmmodel` status and pod logs. This is useful for testing that a model launches correctly before exposing it.

### Subdomain generation

When `endpoints.external.subdomain` is empty, the operator derives it from `metadata.name`: lowercase, replace non-alphanumeric characters with dashes, truncate to 48 characters, append a short hash suffix if truncated.

If `subdomain` is set explicitly, that value is used as-is.

A validating webhook rejects LLMModels if:
- The generated or explicit subdomain collides with an existing LLMModel's subdomain
- The subdomain exceeds the 63-character DNS label limit
- The namespace lacks the `nebari.dev/managed=true` label

The full external hostname is `<subdomain>.llm.<baseDomain>`, where `baseDomain` comes from pack-level Helm values.

### Model revision pinning

For HuggingFace models, `model.revision` pins a specific commit hash or tag (e.g., `main`, `v1.0`, `abc123def`). When empty, the init container downloads the latest version. When set, it passes `--revision <value>` to the HuggingFace download. This makes deployments reproducible - the same LLMModel spec always produces the same model weights.

For OCI sources, use image digests (`@sha256:...`) for true reproducibility. Image tags can move; digests are immutable.

Changes to `model.revision` are treated as breaking changes that trigger a re-download.

## Architecture

### Components deployed by the Helm chart (pack install)

1. **LLMModel CRD** definition
2. **LLM operator** - Go controller (kubebuilder/controller-runtime) watching LLMModel CRs
3. **Key manager** (conditional, `keyManager.enabled`) - web UI + REST API behind a NebariApp with Keycloak/OIDC auth
4. **Envoy AI Gateway** (conditional, `envoyAIGateway.install`) - controller and CRDs; when false, assumes pre-installed
5. **`llm-api-keys` namespace** - dedicated namespace for API key Secrets, isolated from model namespaces

### Resources created by the operator per LLMModel

| Resource | Namespace | Purpose |
|---|---|---|
| PVC | model namespace | Model storage (HuggingFace source, pvc storage type) |
| Deployment (vLLM) | model namespace | Model serving pods with init container for preloading |
| Service | model namespace | ClusterIP for the vLLM pods |
| ServiceAccount | model namespace | Pod identity |
| NetworkPolicy | model namespace | Default-deny ingress, allow only Gateway + EPP + Prometheus |
| InferencePool + EPP | model namespace | Intelligent inference scheduling |
| AIGatewayRoute (external) | model namespace | External endpoint with token counting, rate limiting |
| AIGatewayRoute (internal) | model namespace | Internal endpoint with token counting, rate limiting |
| SecurityPolicy (external) | model namespace | apiKeyAuth with sanitize:true, referencing api-keys Secret via ReferenceGrant |
| SecurityPolicy (internal) | model namespace | JWT validation with group claim matching against access.groups |
| Secret (API keys) | `llm-api-keys` | Per-model API key store (created by operator, data managed by key manager) |
| ConfigMap (key metadata) | `llm-api-keys` | Per-model metadata for API keys (creator, timestamp, description) |
| ReferenceGrant | `llm-api-keys` | Allows SecurityPolicy in model namespace to reference Secret in llm-api-keys |
| PodMonitor | model namespace | Prometheus metrics scraping (when monitoring enabled) |

### Resource ownership

All operator-created resources in the model namespace have an ownerReference back to the LLMModel CR. Deleting the LLMModel garbage-collects these resources.

Resources in the `llm-api-keys` namespace (API key Secret, metadata ConfigMap, ReferenceGrant) cannot use cross-namespace ownerReferences. Instead, the operator labels them with `llm.nebari.dev/model-name` and `llm.nebari.dev/model-namespace`, and a finalizer on the LLMModel ensures cleanup on deletion.

### NetworkPolicy

Every LLMModel gets a NetworkPolicy that enforces default-deny ingress on the model pods, allowing traffic only from:

- **Envoy Gateway pods** (by namespace selector for `envoy-gateway-system`)
- **EPP pods** (by pod selector within the model namespace)
- **Prometheus** (by namespace selector or pod selector for monitoring)

This makes the Gateway the only path to the model, whether endpoints are enabled or not. Direct Service access from other in-cluster workloads is blocked. This is not optional - the NetworkPolicy is always created.

### Key manager RBAC

The key manager runs with a dedicated ServiceAccount. Its RBAC is scoped to two areas:

- **ClusterRole** for LLMModel read access: `get`, `list`, `watch` on `llmmodels.llm.nebari.dev` across all namespaces
- **Role in `llm-api-keys` namespace**: `get`, `list`, `create`, `update`, `patch`, `delete` on Secrets and ConfigMaps in the `llm-api-keys` namespace only

This is a hard RBAC boundary. The key manager can only access Secrets in the dedicated `llm-api-keys` namespace, not in model namespaces or anywhere else. Kubernetes RBAC does not support label-based filtering, so namespace isolation is the enforcement mechanism.

### API key audit

The key manager runs a periodic background audit (configurable interval, default 5 minutes) that:

1. Lists all API key Secrets in `llm-api-keys`
2. For each key entry, looks up the creator's current groups via the OIDC userinfo endpoint
3. If the creator no longer belongs to a group that matches the model's `access.groups`, revokes the key

There is a window (up to the audit interval) where a user who has lost group access can still use existing keys. This is acceptable for v0.1 and consistent with how most RBAC systems handle eventual consistency.

### Resource creation approach

The operator creates Kubernetes resources directly rather than rendering the upstream llm-d Helm charts. This gives us clean ownership (ownerReferences), simpler reconciliation, and no Helm-in-operator complexity.

The tradeoff is tracking upstream changes manually. Each resource template in the operator code must document which llm-d-modelservice chart version it's based on. When llm-d releases updates, we diff their chart and update our resource specs.

### Reconciliation flow

```
LLMModel CR applied
  |
  +-> Validate (webhook: subdomain uniqueness, DNS length, namespace label)
  |
  +-> Phase: Pending
  |
  +-> Create PVC (if HF source + PVC storage type)
  +-> Create API key Secret + metadata ConfigMap in llm-api-keys namespace
  +-> Create ReferenceGrant in llm-api-keys namespace
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

- Hostname: `<subdomain>.llm.<baseDomain>`
- AIGatewayRoute for token counting, rate limiting, protocol normalization
- SecurityPolicy with `apiKeyAuth` attached to the generated HTTPRoute (same name as AIGatewayRoute)
- `sanitize: true` strips the API key before forwarding to vLLM
- `forwardClientIDHeader: X-Client-ID` passes the authenticated client ID downstream for logging and GIE flow control
- API key Secret referenced cross-namespace via ReferenceGrant from `llm-api-keys`

### Internal endpoint

For JupyterLab, in-cluster chat UIs, notebooks.

```
In-cluster app -> Authorization: Bearer <JWT> -> Envoy AI Gateway (internal) -> JWT SecurityPolicy -> InferencePool -> vLLM
```

- Hostname: `<subdomain>.llm-internal.<baseDomain>` (via internal Gateway)
- AIGatewayRoute for token counting, rate limiting, protocol normalization
- SecurityPolicy with `jwt` auth (not OIDC) attached to the generated HTTPRoute
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

## Key manager

A small web application behind NebariApp that lets authenticated users generate and manage API keys for models they can access.

### How it works

1. User hits the key manager UI at `keys.llm.<baseDomain>`
2. Keycloak/OIDC login via NebariApp auth
3. Key manager watches all LLMModel CRs, filters to models where `access.groups` overlaps with the user's OIDC groups (or `access.public: true`)
4. User sees only models they can access
5. User creates a key for a model; key manager generates `sk-<random>`, writes the client ID and key value to that model's `<name>-api-keys` Secret in the `llm-api-keys` namespace, and writes metadata to the corresponding ConfigMap
6. Envoy Gateway's apiKeyAuth picks up the new Secret entry immediately

Revocation: remove the entry from the Secret and its corresponding metadata from the ConfigMap. Immediate effect.

### Known limitation: keys are not continuously tied to group membership

API keys are issued based on the user's groups at creation time. If a user later loses group access, existing keys continue to work until the periodic audit revokes them (default: 5 minute interval). This is stated plainly because it is not the same as ongoing group-based authorization. For v0.1, this eventual consistency is acceptable.

### Data model

No database. State is split across two Kubernetes resources per model, both in the `llm-api-keys` namespace:

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

auth:
  oidc:
    issuerURL: ""
    groupsClaim: groups
    audience: ""

envoyAIGateway:
  install: true

keyManager:
  enabled: true
  image: ghcr.io/nebari-dev/nebari-llm-serving-pack/key-manager:v0.1.0
  auditInterval: 5m

operator:
  image: ghcr.io/nebari-dev/nebari-llm-serving-pack/operator:v0.1.0

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
    targetRevision: v0.1.0
    helm:
      valueFiles:
        - values-prod.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: llm-serving
```

2. ArgoCD deploys the pack: CRD, operator, key manager, AI Gateway, `llm-api-keys` namespace
3. Admin applies LLMModel CRs (directly or via a second ArgoCD Application for GitOps)
4. Operator reconciles each LLMModel through its phases
5. Users access the key manager UI to generate API keys, or use their JWT for in-cluster access

## Example: two models with different access

```yaml
# Model available to all authenticated users
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: devstral-32b
  namespace: llm-serving
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
  namespace: llm-serving
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

## Multi-namespace support

LLMModels can be deployed in any namespace with the `nebari.dev/managed=true` label. This enables team isolation: the data science team deploys models in `llm-data-science`, the engineering team in `llm-engineering`.

**Operator**: single instance deployed by the Helm chart, with a ClusterRole that watches LLMModel CRs across all namespaces with `nebari.dev/managed=true`. It creates resources in the same namespace as the LLMModel (except API key Secrets, which go to `llm-api-keys`). One LLMModel failing reconciliation does not block others - errors are isolated per CR.

**Key manager**: single instance with a ClusterRole for LLMModel read access and a Role scoped to the `llm-api-keys` namespace for Secret/ConfigMap management. A user who belongs to groups with access to models in multiple namespaces sees all of them in the UI, grouped by namespace.

**API key isolation**: all API key Secrets live in the dedicated `llm-api-keys` namespace. This provides a hard RBAC boundary - the key manager's ServiceAccount can only access Secrets in this namespace, not in model namespaces. ReferenceGrants allow Envoy Gateway SecurityPolicies in model namespaces to reference these Secrets cross-namespace.

**Namespace creation**: the operator does not create model namespaces. Admins create namespaces and label them `nebari.dev/managed=true` before applying LLMModels. The `llm-api-keys` namespace is created by the Helm chart.

## Security model

**LLMModel creation**: any user with RBAC access to create LLMModel resources in a managed namespace can deploy a model. The `advanced` fields are explicitly typed and scoped to safe operations (scheduling constraints, env vars, extra CLI args). Security-sensitive pod spec fields (securityContext, hostNetwork, volumes, etc.) are not exposed.

**Network isolation**: model pods have a default-deny NetworkPolicy. Traffic is only allowed from the Envoy Gateway data plane, the EPP, and Prometheus. Direct Service access is blocked for all in-cluster workloads.

**Secret isolation**: API key Secrets are in a dedicated namespace with namespace-scoped RBAC. The key manager is the only non-operator component with access to these Secrets.

**Gateway as security boundary**: all model access (external and internal) flows through Envoy Gateway, where auth is enforced via SecurityPolicy. The external endpoint uses apiKeyAuth with `sanitize: true` (API keys are stripped before reaching vLLM). The internal endpoint uses JWT validation against the OIDC issuer's JWKS endpoint.

## Open questions

None remaining. All design decisions have been made.
