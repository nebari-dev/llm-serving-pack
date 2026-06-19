# nebari-llm-serving-pack

A [Nebari](https://github.com/nebari-dev/nebari-infrastructure-core) software pack for serving LLMs. Deploys a Kubernetes operator that manages LLM model serving via [llm-d](https://llm-d.ai), with per-model access control, API key management, and Envoy AI Gateway integration for token counting and rate limiting.

**Documentation:** [https://nebari-dev.github.io/nebari-llm-serving-pack/](https://nebari-dev.github.io/nebari-llm-serving-pack/)

## What this does

You apply an `LLMModel` custom resource and the operator handles the rest: model download, vLLM serving pods, inference scheduling, routing, and auth.

Each model gets per-model access control via OIDC groups (works with any OIDC provider, tested against Keycloak). Two auth endpoints are created per model: external access via API keys, and internal (in-cluster) access via JWT. Both paths go through Envoy AI Gateway for token counting and rate limiting.

An optional key manager web UI lets users generate and revoke API keys for models they have access to.

Models can be loaded from HuggingFace (default) or mounted as OCI/modelcar images. Model downloads use a purpose-built [distroless container image](model-downloader/) with pixi-managed dependencies for reproducibility.

## Quick start

### Prerequisites

- Kubernetes 1.28+ cluster with [Nebari Infrastructure Core](https://github.com/nebari-dev/nebari-infrastructure-core) deployed
- [nebari-operator](https://github.com/nebari-dev/nebari-operator) running
- NVIDIA GPU Operator installed (auto-discovers GPU nodes and manages the device plugin). **Note**: nebari-infrastructure-core does not install this automatically yet - tracked in [nebari-dev/nebari-infrastructure-core#232](https://github.com/nebari-dev/nebari-infrastructure-core/issues/232). Until that is done, install it manually as an ArgoCD app (see [examples/nvidia-gpu-operator.yaml](examples/nvidia-gpu-operator.yaml)).
- **Envoy Gateway installed and configured for AI Gateway integration** - `extensionApis.enableBackend`, `extensionManager` pointing at the AI Gateway controller service, and `backendResources` allowing `inference.networking.k8s.io/InferencePool`. This is a **hard requirement**; without it, the routing layer 404s at runtime. Ready-to-apply example in [`examples/envoy-gateway.yaml`](examples/envoy-gateway.yaml); see the [Installation guide](https://nebari-dev.github.io/nebari-llm-serving-pack/installation/#6-reconfigure-envoy-gateway-with-ai-gateway-extension-wiring) for details.
- Envoy AI Gateway installed (v0.5.0+). **Note**: the `envoyAIGateway.install` flag in this chart is not yet implemented - tracked in [#44](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/44). Until that is done, install it manually as an ArgoCD app (see [examples/envoy-ai-gateway.yaml](examples/envoy-ai-gateway.yaml)).
- [Gateway API Inference Extension](https://github.com/kubernetes-sigs/gateway-api-inference-extension) (GIE) installed (InferencePool / InferenceModel CRDs).
- A cert-manager `ClusterIssuer` the operator can use for the shared-hostname Certificate. Default expected name is `letsencrypt-production`; override with `platform.tls.clusterIssuer` in the chart values.
- DNS for `llm.<baseDomain>` and `llm-internal.<baseDomain>` resolving to the shared Gateway's load balancer (a wildcard CNAME on the base domain is the simplest way). Required for HTTP-01 issuance on the shared Certificate.
- A StorageClass for model storage (EFS, EBS gp3, or any CSI-backed storage that can provision PVCs large enough for your models).

### Deploy the pack

The pack is deployed as an ArgoCD Application. A multi-source setup lets you keep model definitions in a separate Git repo from the pack itself:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: nebari-llm-serving
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "7"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: foundational

  sources:
    # Source 1: LLM serving pack Helm chart
    - repoURL: https://github.com/nebari-dev/nebari-llm-serving-pack.git
      targetRevision: v0.1.0-alpha.7
      path: charts/nebari-llm-serving
      helm:
        releaseName: nebari-llm-serving
        values: |
          platform:
            baseDomain: "your-cluster.example.com"
            gateway:
              external:
                name: nebari-gateway
                namespace: envoy-gateway-system
              internal:
                name: nebari-gateway
                namespace: envoy-gateway-system
              # Operator patches its own HTTPS listeners onto the shared
              # Gateway for llm.<baseDomain> + llm-internal.<baseDomain>.
              # Pre-existing listeners are matched by name and left alone.
              manageSharedListeners: true
            tls:
              # Must name a cert-manager ClusterIssuer that already
              # exists on the cluster. HTTP-01 is the assumed challenge
              # type; no wildcards required.
              clusterIssuer: letsencrypt-production

          defaults:
            storage:
              storageClassName: efs-sc  # or gp3, longhorn, etc.

          auth:
            oidc:
              issuerURL: "https://keycloak.your-cluster.example.com/realms/nebari"
              groupsClaim: groups

          keyManager:
            enabled: true

    # Source 2: LLMModel CRs from your cluster config repo
    - repoURL: https://github.com/your-org/your-cluster-config.git
      targetRevision: main
      path: clusters/your-cluster/manifests/llm-models

  destination:
    server: https://kubernetes.default.svc
    namespace: nebari-llm-serving-system

  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
      - SkipDryRunOnMissingResource=true
    retry:
      limit: 5
      backoff:
        duration: 5s
        factor: 2
        maxDuration: 3m
```

### Deploy a model

Add an `LLMModel` resource to your cluster config repo (the path referenced by Source 2 above):

```yaml
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: qwen3-5-35b-a3b-gptq-int4
  namespace: nebari-llm-serving-system
spec:
  model:
    name: "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4"
    source: huggingface
    storage:
      type: pvc
      size: "30Gi"
      # storageClassName: efs-sc  # optional, overrides the pack default
  resources:
    gpu:
      count: 1
      type: nvidia
    requests:
      cpu: "2"
      memory: "8Gi"
    limits:
      cpu: "4"
      memory: "12Gi"
  serving:
    replicas: 1
    tensorParallelism: 1
    vllmArgs:
      - "--quantization"
      - "gptq_marlin"
      - "--max-model-len"
      - "8192"
  access:
    public: false
    groups:
      - "llm"
  endpoints:
    external:
      enabled: true
    internal:
      enabled: true
```

For gated models that require authentication, create a Secret with your HuggingFace token and reference it:

```yaml
spec:
  model:
    authSecretName: hf-token  # Secret with key "HF_TOKEN"
```

### Use the model

All models on the cluster share one hostname pair; clients dispatch by setting the `model` field in the request body (same as an OpenAI API call).

External (API key):
```bash
curl https://llm.your-cluster.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4", "messages": [{"role": "user", "content": "Hello"}]}'
```

Internal (JWT from JupyterLab or in-cluster service):
```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="https://llm-internal.your-cluster.example.com/v1",
    api_key=os.environ["JUPYTERHUB_API_TOKEN"],  # JWT from Nebari
)
response = client.chat.completions.create(
    model="Qwen/Qwen3.5-35B-A3B-GPTQ-Int4",
    messages=[{"role": "user", "content": "Hello"}],
)
```

## Helm values reference

| Value | Description | Default |
|-------|-------------|---------|
| `platform.baseDomain` | Base domain for the Nebari deployment (required) | `""` |
| `platform.gateway.external.name` | Name of the external Gateway resource | `nebari-gateway` |
| `platform.gateway.external.namespace` | Namespace of the external Gateway | `envoy-gateway-system` |
| `platform.gateway.internal.name` | Name of the internal Gateway resource | `nebari-internal-gateway` |
| `platform.gateway.internal.namespace` | Namespace of the internal Gateway | `envoy-gateway-system` |
| `auth.oidc.issuerURL` | OIDC issuer URL (static value, or read from Secret if empty) | `""` |
| `auth.oidc.groupsClaim` | JWT claim containing group memberships | `groups` |
| `auth.oidc.audience` | Expected JWT audience (empty = no audience check) | `""` |
| `defaults.serving.image` | Default vLLM serving image | `ghcr.io/llm-d/llm-d-cuda:v0.7.0` |
| `defaults.epp.image` | Endpoint Picker (llm-d-inference-scheduler) image | `ghcr.io/llm-d/llm-d-inference-scheduler:v0.8.0` |
| `defaults.storage.storageClassName` | Default StorageClass for model PVCs (empty = cluster default) | `""` |
| `defaults.monitoring.enabled` | Enable PodMonitor for Prometheus scraping | `true` |
| `keyManager.enabled` | Deploy the key manager web UI | `true` |

## Architecture

```
Admin applies LLMModel CR
        |
        v
  LLM Operator (watches CRDs across all managed namespaces)
        |
        +---> PVC + model-downloader init container (HuggingFace download)
        +---> vLLM Deployment + Service
        +---> InferencePool + EPP (intelligent scheduling)
        +---> AIGatewayRoute + SecurityPolicy (external, API key auth)
        +---> AIGatewayRoute + SecurityPolicy (internal, OIDC auth)
        |
  Key Manager (optional)
        |
        +---> Web UI behind NebariApp (Keycloak/OIDC login)
        +---> Generates API keys, writes to K8s Secrets
        +---> Envoy Gateway validates keys natively
```

### Container images

| Image | Description |
|-------|-------------|
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/operator` | LLM operator - reconciles LLMModel CRDs |
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/key-manager` | Key manager web UI and API |
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/model-downloader` | Model download init container (distroless, pixi-managed) |

### Infrastructure requirements

The pack expects the following to be available on the cluster:

- **GPU Operator**: The [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/overview.html) must be installed so that GPU nodes advertise `nvidia.com/gpu` as an allocatable resource. If your nodes use a pre-installed NVIDIA driver AMI (like AWS `AL2023_x86_64_NVIDIA`), configure the operator with `driver.enabled=false` and `toolkit.enabled=false`.
- **NVIDIA driver 580+**: The default serving image (`llm-d-cuda:v0.7.0`, from llm-d v0.7.0) ships the CUDA 13.0.2 runtime and requires NVIDIA driver branch 580 or later on every GPU node. Nodes on an older driver must be upgraded first, or vLLM will fail to start.
- **Storage**: A StorageClass capable of provisioning PVCs sized for your models. EFS (`efs.csi.aws.com`) is recommended on AWS for its ReadWriteMany support and independence from node disk size. Set the StorageClass name via `defaults.storage.storageClassName`.
- **Gateway**: Envoy Gateway with the Gateway API and AI Gateway extensions. Typically deployed by nebari-infrastructure-core.
- **OIDC provider**: Keycloak or any OIDC-compliant provider for auth. The pack reads the issuer URL from either the Helm value or a Kubernetes Secret provisioned by the nebari-operator.

## Development

See the [Local Development guide](https://nebari-dev.github.io/nebari-llm-serving-pack/local-development/) for a full walkthrough of the local dev environment.

```bash
# Create kind cluster with all dependencies
cd dev && make setup

# Build and load images into the cluster
make build-images && make load-images

# Deploy operator and key manager
make deploy

# Apply a test model
make apply-test-model

# Watch reconciliation
kubectl -n llm-serving get llmmodels -w

# Tail logs
make logs-operator
make logs-key-manager

# Tear down
make teardown
```

Run tests directly:

```bash
cd operator && make test
cd key-manager && go test ./...
```

## Known Limitations

This pack is in beta (v0.1). The following limitations apply:

- **Single-namespace model:** All LLMModels must be in the operator's own namespace. Per-team isolation requires running separate pack installs (one namespace per team). This is a hard constraint imposed by Envoy Gateway's `apiKeyAuth`, which does not support cross-namespace Secret references.
- **API keys are not continuously tied to group membership.** Keys are issued based on the user's OIDC groups at creation time. If a user later loses group access, existing keys continue to work until the periodic audit runs (default interval: 5 minutes). This is eventual consistency, not real-time revocation.
- **JWKS path is Keycloak-specific.** The internal endpoint's JWT SecurityPolicy constructs the JWKS URI as `<issuerURL>/protocol/openid-connect/certs`. Non-Keycloak OIDC providers will not work out of the box. Tracked in [#61](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/61).
- **NVIDIA GPUs only.** AMD and Intel accelerators are not supported in v0.1.
- **No scale-to-zero.** Idle model pods are not scaled down automatically.
- **No per-key rate limiting or token quotas.** Rate limiting is applied at the model level via Envoy AI Gateway, not per individual API key.
- **No API key expiration.** Keys do not expire on a schedule; revocation requires either manual deletion or the audit losing group access.
- **No team-level shared API keys.** Each key is tied to an individual user's identity.
- **OCI model loading uses init-container copy.** Kubernetes image volumes (alpha/beta) are not used; every pod start copies files from the OCI image to a shared emptyDir. On pods with `storage.type: emptyDir` and `preload: true`, every restart triggers a full re-download.
- **API key and metadata storage is limited to ~1 MiB per model** (Kubernetes Secret/ConfigMap size limit). This supports several thousand keys per model; it is a known scaling ceiling for v0.1.
- **`envoyAIGateway.install` flag is not yet implemented.** Envoy AI Gateway must be installed separately. Tracked in [#44](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/44).
- **Per-model subdomains are not yet wired into routing.** The `endpoints.external.subdomain` field is validated but unused. All models share `llm.<baseDomain>`; per-model dispatch is by the `model` field in the request body.

## License

Apache 2.0. See [LICENSE](LICENSE).
