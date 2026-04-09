# nebari-llm-serving-pack

A [Nebari](https://github.com/nebari-dev/nebari-infrastructure-core) software pack for serving LLMs. Deploys a Kubernetes operator that manages LLM model serving via [llm-d](https://llm-d.ai), with per-model access control, API key management, and Envoy AI Gateway integration for token counting and rate limiting.

## What this does

You apply an `LLMModel` custom resource and the operator handles the rest: model download, vLLM serving pods, inference scheduling, routing, and auth.

Each model gets per-model access control via OIDC groups (works with any OIDC provider, tested against Keycloak). Two auth endpoints are created per model: external access via API keys, and internal (in-cluster) access via JWT. Both paths go through Envoy AI Gateway for token counting and rate limiting.

An optional key manager web UI lets users generate and revoke API keys for models they have access to.

Models can be loaded from HuggingFace (default) or mounted as OCI/modelcar images. Model downloads use a purpose-built [distroless container image](model-downloader/) with pixi-managed dependencies for reproducibility.

## Quick start

### Prerequisites

- Kubernetes 1.28+ cluster with [Nebari Infrastructure Core](https://github.com/nebari-dev/nebari-infrastructure-core) deployed
- [nebari-operator](https://github.com/nebari-dev/nebari-operator) running
- NVIDIA GPU Operator installed (auto-discovers GPU nodes and manages the device plugin)
- Envoy Gateway installed (deployed by nebari-infrastructure-core)
- A StorageClass for model storage (EFS, EBS gp3, or any CSI-backed storage that can provision PVCs large enough for your models)

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
      targetRevision: main
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
  namespace: llm-serving
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
      - "gptq"
      - "--max-model-len"
      - "8192"
  access:
    public: false
    groups:
      - "llm"
  endpoints:
    external:
      enabled: true
      subdomain: qwen3-5-35b
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

External (API key):
```bash
curl https://qwen3-5-35b.your-cluster.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4", "messages": [{"role": "user", "content": "Hello"}]}'
```

Internal (JWT from JupyterLab or in-cluster service):
```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://qwen3-5-35b-a3b-gptq-int4.llm-serving.svc.cluster.local/v1",
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
| `defaults.serving.image` | Default vLLM serving image | `ghcr.io/llm-d/llm-d-cuda:v0.6.0` |
| `defaults.storage.storageClassName` | Default StorageClass for model PVCs (empty = cluster default) | `""` |
| `defaults.monitoring.enabled` | Enable PodMonitor for Prometheus scraping | `true` |
| `keyManager.enabled` | Deploy the key manager web UI | `true` |
| `apiKeysNamespace` | Namespace where API key Secrets are stored | `llm-api-keys` |

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
- **Storage**: A StorageClass capable of provisioning PVCs sized for your models. EFS (`efs.csi.aws.com`) is recommended on AWS for its ReadWriteMany support and independence from node disk size. Set the StorageClass name via `defaults.storage.storageClassName`.
- **Gateway**: Envoy Gateway with the Gateway API and AI Gateway extensions. Typically deployed by nebari-infrastructure-core.
- **OIDC provider**: Keycloak or any OIDC-compliant provider for auth. The pack reads the issuer URL from either the Helm value or a Kubernetes Secret provisioned by the nebari-operator.

## Development

See [docs/getting-started.md](docs/getting-started.md) for a full walkthrough of the local dev environment.

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

## License

Apache 2.0. See [LICENSE](LICENSE).
