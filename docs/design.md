# Nebari LLM serving pack design

## Overview

A Nebari software pack for serving LLMs using llm-d with modelcar/OCI support for model distribution. The pack deploys a Go operator that watches a custom `LLMModel` CRD. Admins apply one LLMModel per model they want to serve. The operator handles everything downstream: model storage, vLLM serving pods, inference scheduling, routing, and access control.

An optional key manager service gives users a web UI to generate API keys for external access. Envoy AI Gateway provides token counting, rate limiting, and protocol normalization on both external and internal endpoints.

## Goals

- Admins deploy the pack once via ArgoCD, then declare models as LLMModel CRs
- Per-model access control via OIDC groups, generic for any OIDC provider, tested against Keycloak
- Two auth paths: API keys for external consumers, JWT for in-cluster users
- Model loading from HuggingFace (default) or OCI/modelcar images
- Intelligent inference scheduling via llm-d's InferencePool and EPP
- Token counting and rate limiting on both endpoints via Envoy AI Gateway

## Non-goals for v0.1

- Prefill/decode disaggregation
- KV cache offloading
- Multi-accelerator support (AMD, Intel, etc.) - NVIDIA only initially
- Usage billing or cost chargeback
- Scale-to-zero autoscaling
- Per-key rate limiting or token quotas
- API key expiration
- Team-level shared API keys

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
    # image: registry.example.com/models/devstral:v1  # oci only
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
    image: ghcr.io/llm-d/llm-d-cuda:v0.5.1
    replicas: 1
    tensorParallelism: 2             # defaults to gpu.count
    dataParallelism: 1
    vllmArgs:
      - "--gpu-memory-utilization=0.95"
      - "--max-model-len=32768"
    monitoring:
      enabled: true

  access:
    groups:                          # OIDC groups that can access this model
      - everyone

  endpoints:
    external:
      enabled: true
      subdomain: ""                  # optional override; defaults to slugified metadata.name
    internal:
      enabled: true

  advanced:
    vllmOverrides: {}                # raw overrides merged into the vLLM Deployment spec
    inferencePoolOverrides: {}       # raw overrides merged into the InferencePool spec
```

### Advanced overrides

The `advanced` section provides escape hatches for llm-d configuration not surfaced as first-class CRD fields. Each override targets a specific resource type:

- `vllmOverrides`: merged into the vLLM Deployment spec (containers, volumes, tolerations, etc.)
- `inferencePoolOverrides`: merged into the InferencePool spec

Merge strategy: override values win over operator-generated values. If an override conflicts with a first-class CRD field (e.g., setting replicas in both `serving.replicas` and `vllmOverrides`), the first-class field takes precedence and the operator logs a warning.

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
    internal: "http://devstral-32b.llm-serving.svc.cluster.local"
```

Phase semantics:
- **Pending**: CR accepted, operator starting reconciliation
- **Downloading**: model download in progress (HF source only)
- **Starting**: model downloaded, vLLM pods starting up
- **Ready**: all replicas ready, endpoints configured
- **Degraded**: some but not all replicas ready; model still serving traffic
- **Error**: reconciliation failed, check conditions for details

When both endpoints are disabled, the operator still creates the vLLM Deployment and InferencePool but skips route and SecurityPolicy creation. The model serves traffic only to in-cluster consumers that address the Service directly (no auth enforcement in this case).

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

For OCI sources, the image tag on `model.image` already serves this purpose.

## Architecture

### Components deployed by the Helm chart (pack install)

1. **LLMModel CRD** definition
2. **LLM operator** - Go controller (kubebuilder/controller-runtime) watching LLMModel CRs
3. **Key manager** (conditional, `keyManager.enabled`) - web UI + REST API behind a NebariApp with Keycloak/OIDC auth
4. **Envoy AI Gateway** (conditional, `envoyAIGateway.install`) - controller and CRDs; when false, assumes pre-installed

### Resources created by the operator per LLMModel

| Resource | Purpose |
|---|---|
| PVC | Model storage (HuggingFace source, pvc storage type) |
| Deployment (vLLM) | Model serving pods with init container for preloading |
| Service | ClusterIP for the vLLM pods |
| ServiceAccount | Pod identity |
| InferencePool + EPP | Intelligent inference scheduling (prefix-cache-aware routing, load balancing) |
| AIGatewayRoute (external) | External endpoint with token counting, rate limiting |
| AIGatewayRoute (internal) | Internal endpoint with token counting, rate limiting |
| SecurityPolicy (external) | apiKeyAuth referencing the model's API key Secret |
| SecurityPolicy (internal) | OIDC JWT with group claim matching against access.groups |
| Secret (API keys) | Per-model API key store (created by operator, data managed by key manager) |
| ConfigMap (key metadata) | Per-model metadata for API keys (creator, timestamp, description) |
| PodMonitor | Prometheus metrics scraping (when monitoring enabled) |

### Resource ownership

All operator-created resources have an ownerReference back to the LLMModel CR. Deleting the LLMModel garbage-collects everything, including the API key Secret and metadata ConfigMap.

The operator creates the API key Secret (`<model-name>-api-keys`) and metadata ConfigMap (`<model-name>-api-key-metadata`) with ownerReferences to the LLMModel. The key manager has RBAC to read/write data entries in both, but does not own them. This avoids a race condition where the key manager tries to write a key before the operator has created the Secret, and ensures cleanup on model deletion is automatic.

### Key manager RBAC

The key manager runs with a dedicated ServiceAccount and a ClusterRole (to operate across all managed namespaces). The ClusterRole grants:

- `get`, `list`, `watch` on `llmmodels.llm.nebari.dev` - to discover available models and their access groups
- `get`, `update`, `patch` on Secrets matching label `llm.nebari.dev/api-keys=true` - to add/remove API key entries
- `get`, `update`, `patch` on ConfigMaps matching label `llm.nebari.dev/api-key-metadata=true` - to manage key metadata

The operator labels the Secrets and ConfigMaps it creates so the key manager's label-selector RBAC works. The key manager cannot access arbitrary Secrets in any namespace.

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
  +-> Create API key Secret + metadata ConfigMap (with labels for key manager RBAC)
  |
  +-> Create vLLM Deployment with init container for model download
  |
  +-> Phase: Downloading
  |     (init container downloading model; operator watches pod phase)
  |
  +-> Init container completes, vLLM container starts
  |
  +-> Phase: Starting
  |     (wait for vLLM pods ready via /v1/models probe)
  |
  +-> Create InferencePool + EPP
  +-> Create AIGatewayRoutes (external + internal)
  +-> Create SecurityPolicies (apiKeyAuth + OIDC)
  |
  +-> Phase: Ready
```

The init container and vLLM container are part of the same Deployment. The init container blocks pod startup until the model is downloaded. There is no separate Job resource. The operator watches pod phase transitions to track progress through Downloading -> Starting -> Ready.

### Spec updates and rolling restarts

When a running LLMModel's spec changes, the operator updates the corresponding resources in place. For changes that affect the vLLM Deployment (image, vllmArgs, resources, replicas), the operator updates the Deployment spec and Kubernetes handles the rolling restart. For changes to access groups or endpoints, the operator updates SecurityPolicies and routes without touching the Deployment.

Changes to `model.name`, `model.source`, or `model.storage` require a new model download. The operator detects these as breaking changes, drains the existing pods, and re-triggers the download phase.

## Dual endpoint auth

Each LLMModel gets two endpoints with different auth mechanisms. Per-model hostnames ensure access control is enforced at the gateway level without request body inspection.

### External endpoint

For CI pipelines, external applications, anything outside the cluster.

```
Client -> Authorization: Bearer sk-... -> Envoy AI Gateway -> apiKeyAuth SecurityPolicy -> InferencePool -> vLLM
```

- Hostname: `<subdomain>.llm.<baseDomain>`
- Per-model SecurityPolicy references that model's `<name>-api-keys` Secret
- `forwardClientIDHeader: X-Client-ID` passes the authenticated client ID downstream for logging and GIE flow control
- Envoy AI Gateway provides token counting and rate limiting

### Internal endpoint

For JupyterLab, in-cluster chat UIs, notebooks.

```
In-cluster app -> Authorization: Bearer <JWT> -> Envoy AI Gateway (internal) -> OIDC SecurityPolicy -> InferencePool -> vLLM
```

- Exposed via the internal Envoy Gateway
- Per-model SecurityPolicy with OIDC configuration, group claim validation against the LLMModel's `access.groups`
- Same InferencePool backend as external; both routes hit the same vLLM pods
- Envoy AI Gateway provides token counting and rate limiting here too

Note on JWT availability: the internal endpoint assumes in-cluster services can forward the user's JWT. In Nebari, JupyterHub injects tokens into user pods via environment variables. For other in-cluster services (chat UIs, etc.), the application must handle OIDC login and forward the resulting token. If a service cannot obtain a JWT, it should use the external endpoint with an API key instead.

### OIDC configuration

Pack-level Helm values, not per-model:

```yaml
auth:
  oidc:
    issuerURL: ""                    # e.g., https://keycloak.nebari.example.com/realms/nebari
    groupsClaim: groups              # JWT claim containing group memberships
    audienceClaim: ""                # optional audience validation
```

The operator combines these pack-level values with each LLMModel's `access.groups` to generate per-model OIDC SecurityPolicies. This is generic for any OIDC provider but tested against Keycloak.

## Key manager

A small web application behind NebariApp that lets authenticated users generate and manage API keys for models they can access.

### How it works

1. User hits the key manager UI at `keys.llm.<baseDomain>`
2. Keycloak/OIDC login via NebariApp auth
3. Key manager watches all LLMModel CRs, filters to models where `access.groups` overlaps with the user's OIDC groups
4. User sees only models they can access
5. User creates a key for a model; key manager generates `sk-<random>`, writes the client ID and key value to that model's `<name>-api-keys` Secret, and writes metadata (creator, timestamp, description) to the `<name>-api-key-metadata` ConfigMap
6. Envoy Gateway's apiKeyAuth picks up the new Secret entry immediately

Revocation: remove the entry from the Secret and its corresponding metadata from the ConfigMap. Immediate effect.

### Data model

No database. State is split across two Kubernetes resources per model:

- **Secret** (`<model-name>-api-keys`, labeled `llm.nebari.dev/api-keys=true`): contains only the data Envoy Gateway needs. Each entry: key = client ID (e.g., `user-chuck-1`), value = the API key. This Secret is the source of truth for authentication because Envoy Gateway's apiKeyAuth reads from it natively.

- **ConfigMap** (`<model-name>-api-key-metadata`, labeled `llm.nebari.dev/api-key-metadata=true`): contains a JSON blob per client ID with management metadata (creator username, creation timestamp, description, last used hint). Separated from the Secret so the key manager can read/display metadata without granting the UI read access to the actual key values after creation.

### API key Secret format

Envoy Gateway's apiKeyAuth expects Secret data entries where each key is the client ID and each value is the raw API key string. The key manager generates keys in the format `sk-<32-char-base64url-random>`. Example Secret data:

```yaml
data:
  user-chuck-1: c2stYWJjMTIzZGVmNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4  # base64 of "sk-abc123def..."
  user-alice-1: c2stZGVmNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4OTBhYmNk
```

When extracting from the `Authorization` header, the `Bearer` prefix is stripped by the client. The stored value is the full `sk-...` string.

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
When `install: false`: pack only creates AIGatewayRoute and AIServiceBackend resources. Fails with a clear error if CRDs are missing.

Both external and internal endpoints use AIGatewayRoute, giving both paths token counting, rate limiting, and protocol normalization. This also means the usage data is available for future cost tracking on both paths.

## Model loading

Two supported sources, selectable per LLMModel.

### HuggingFace (default)

The operator creates a PVC (when `storage.type: pvc`) and a Deployment with an init container that downloads the model using the HuggingFace token from the referenced Secret. The init container writes to the PVC. The vLLM container mounts the same PVC and starts with the model already on disk.

This solves the startup timeout problem: large models (400GB+) can take 30-60 minutes to download. The init container has no probe timeout. The vLLM container's startup probe only fires after the init container succeeds, at which point the model is local and startup is fast.

PVC storage means the model survives pod restarts and rescheduling without re-downloading. emptyDir is available for small models where persistence isn't needed.

Note: when `storage.type: emptyDir` and `preload: true`, every pod restart triggers a full re-download. The validating webhook emits a warning (not a rejection) for this combination with models over 20GB, since it is likely unintentional for large models.

### OCI/modelcar

The operator sets `model.image` on the vLLM pod spec. On Kubernetes 1.31+ with the ImageVolume feature gate, the OCI image is mounted directly as a read-only volume (no copy). On older clusters, an init container pulls the image and copies model files to a shared volume.

OCI is faster after first pull (node image cache), needs no HuggingFace token, and gives you image signing and versioning via standard container tooling.

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
    audienceClaim: ""

envoyAIGateway:
  install: true

keyManager:
  enabled: true
  image: ghcr.io/nebari-dev/nebari-llm-serving-pack/key-manager:latest

operator:
  image: ghcr.io/nebari-dev/nebari-llm-serving-pack/operator:latest

defaults:
  serving:
    image: ghcr.io/llm-d/llm-d-cuda:v0.5.1
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

These defaults apply to all LLMModels unless overridden in the individual CR spec.

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
    targetRevision: main
    helm:
      valueFiles:
        - values-prod.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: llm-serving
```

2. ArgoCD deploys the pack: CRD, operator, key manager, AI Gateway
3. Admin applies LLMModel CRs (directly or via a second ArgoCD Application for GitOps)
4. Operator reconciles each LLMModel through its phases
5. Users access the key manager UI to generate API keys, or use their JWT for in-cluster access

## Example: two models with different access

```yaml
# Cheap model, everyone gets access
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
    vllmArgs:
      - "--gpu-memory-utilization=0.95"
  access:
    groups:
      - everyone
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

After applying these, a user in the `data-science` group can access both models. A user in `developers` (but not `data-science`) can only access Devstral. The key manager UI shows each user only the models they're allowed to use.

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
      webhook/
        validation.go
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

LLMModels can be deployed in any namespace with the `nebari.dev/managed=true` label. This enables team isolation: the data science team deploys models in `llm-data-science`, the engineering team in `llm-engineering`. Secrets are isolated by namespace, so one team's API keys are not accessible to another team's ServiceAccounts.

**Operator**: single instance deployed by the Helm chart, with a ClusterRole that watches LLMModel CRs across all namespaces with `nebari.dev/managed=true`. It creates resources in the same namespace as the LLMModel. One LLMModel failing reconciliation does not block others - errors are isolated per CR.

**Key manager**: single instance with a ClusterRole scoped to namespaces with `nebari.dev/managed=true`. A user who belongs to groups with access to models in multiple namespaces sees all of them in the UI, grouped by namespace. This is intentional - an individual data scientist might need keys for models managed by different teams.

**Namespace creation**: the operator does not create namespaces. Admins create namespaces and label them `nebari.dev/managed=true` before applying LLMModels. This keeps namespace lifecycle as an explicit admin action, consistent with how NebariApp works.

## Open questions

None remaining. All design decisions have been made.
