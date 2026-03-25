# nebari-llm-serving-pack

A [Nebari](https://github.com/nebari-dev/nebari-infrastructure-core) software pack for serving LLMs. Deploys a Kubernetes operator that manages LLM model serving via [llm-d](https://llm-d.ai), with per-model access control, API key management, and Envoy AI Gateway integration for token counting and rate limiting.

## What this does

You apply an `LLMModel` custom resource and the operator handles the rest: model download, vLLM serving pods, inference scheduling, routing, and auth.

Each model gets per-model access control via OIDC groups (works with any OIDC provider, tested against Keycloak). Two auth endpoints are created per model: external access via API keys, and internal (in-cluster) access via JWT. Both paths go through Envoy AI Gateway for token counting and rate limiting.

An optional key manager web UI lets users generate and revoke API keys for models they have access to.

Models can be loaded from HuggingFace (default) or mounted as OCI/modelcar images.

## Quick start

### Prerequisites

- Kubernetes 1.28+ cluster with [Nebari Infrastructure Core](https://github.com/nebari-dev/nebari-infrastructure-core) deployed
- [nebari-operator](https://github.com/nebari-dev/nebari-operator) running
- NVIDIA GPUs available on worker nodes
- Envoy AI Gateway (installed by this pack, or pre-installed)

### Deploy the pack

Create an ArgoCD Application:

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

### Deploy a model

Create a namespace (if it doesn't exist) and label it:

```bash
kubectl create namespace llm-serving
kubectl label namespace llm-serving nebari.dev/managed=true
```

Apply an LLMModel:

```yaml
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
    replicas: 1
    tensorParallelism: 1
  access:
    groups:
      - everyone
  endpoints:
    external:
      enabled: true
      subdomain: devstral
    internal:
      enabled: true
```

### Use the model

External (API key):
```bash
curl https://devstral.llm.nebari.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "mistralai/Devstral-Small-2505", "messages": [{"role": "user", "content": "Hello"}]}'
```

Internal (JWT from JupyterLab or in-cluster service):
```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://devstral-32b.llm-serving.svc.cluster.local/v1",
    api_key=os.environ["JUPYTERHUB_API_TOKEN"],  # JWT from Nebari
)
response = client.chat.completions.create(
    model="mistralai/Devstral-Small-2505",
    messages=[{"role": "user", "content": "Hello"}],
)
```

## Configuration

See [docs/design.md](docs/design.md) for the full design document, including:

- Complete LLMModel CRD spec and status fields
- Pack-level Helm values reference
- Dual endpoint auth architecture
- Key manager design
- Multi-namespace support
- Model loading strategies

## Architecture

```
Admin applies LLMModel CR
        |
        v
  LLM Operator (watches CRDs across all managed namespaces)
        |
        +---> PVC + init container (model download)
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

## Development

```bash
# Local dev cluster with kind
cd dev && make setup

# Run operator locally
cd operator && go run ./cmd/main.go

# Run key manager locally
cd key-manager && go run ./cmd/main.go

# Run tests
cd operator && go test ./...
cd key-manager && go test ./...
```

## License

Apache 2.0. See [LICENSE](LICENSE).
