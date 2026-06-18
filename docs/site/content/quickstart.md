+++
title = "Quickstart"
+++

Get a model serving in five minutes. This page covers the fast path: deploy the pack, apply one `LLMModel`, and call the API.

For a full production-grade setup (air-gapped clusters, HuggingFace token secrets, monitoring, and all values), see [Installation](/installation/) and [Configuration](/configuration/).

## Prerequisites

- Kubernetes 1.28+ cluster with [Nebari Infrastructure Core](https://github.com/nebari-dev/nebari-infrastructure-core) deployed
- [nebari-operator](https://github.com/nebari-dev/nebari-operator) running
- NVIDIA GPU Operator installed (auto-discovers GPU nodes). **Note**: nebari-infrastructure-core does not install this automatically yet - see [nebari-dev/nebari-infrastructure-core#232](https://github.com/nebari-dev/nebari-infrastructure-core/issues/232). Until then, install it manually via ArgoCD (see [`examples/nvidia-gpu-operator.yaml`](https://github.com/nebari-dev/nebari-llm-serving-pack/blob/main/examples/nvidia-gpu-operator.yaml)).
- **Envoy Gateway** configured for AI Gateway integration - `extensionApis.enableBackend`, `extensionManager` pointing at the AI Gateway controller service, and `backendResources` allowing `inference.networking.k8s.io/InferencePool`. Ready-to-apply example: [`examples/envoy-gateway.yaml`](https://github.com/nebari-dev/nebari-llm-serving-pack/blob/main/examples/envoy-gateway.yaml). See [Installation](/installation/) for full wiring details.
- **Envoy AI Gateway** v0.5.0+ installed. **Note**: the `envoyAIGateway.install` chart flag is not yet implemented ([#44](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/44)). Install manually via ArgoCD (see [`examples/envoy-ai-gateway.yaml`](https://github.com/nebari-dev/nebari-llm-serving-pack/blob/main/examples/envoy-ai-gateway.yaml)).
- [Gateway API Inference Extension](https://github.com/kubernetes-sigs/gateway-api-inference-extension) (InferencePool / InferenceModel CRDs)
- A cert-manager `ClusterIssuer` for the shared TLS certificate (default name: `letsencrypt-production`; override with `platform.tls.clusterIssuer`)
- DNS for `llm.<baseDomain>` and `llm-internal.<baseDomain>` pointing at the shared Gateway load balancer
- A StorageClass that can provision PVCs large enough for your models (EFS, EBS gp3, or equivalent)

## Deploy the pack

The pack is deployed as an ArgoCD Application. A multi-source setup lets you keep model definitions in a separate Git repo:

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
      targetRevision: v0.1.0-alpha.9
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
              manageSharedListeners: true
            tls:
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

For all available Helm values, see [Configuration](/configuration/).

## Deploy a model

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

For gated HuggingFace models, create a Secret with your HuggingFace token and reference it:

```yaml
spec:
  model:
    authSecretName: hf-token  # Secret with key "HF_TOKEN"
```

The operator handles the rest: model download, vLLM pods, InferencePool, routing, and auth. Watch progress with:

```bash
kubectl -n nebari-llm-serving-system get llmmodels -w
```

For the full CRD reference including all `spec` fields, see [Configuration](/configuration/).

## Use the model

All models on the cluster share one hostname pair. Clients select a model via the `model` field in the request body, matching the OpenAI API convention.

### External access (API key)

Generate a key via the key manager UI (`https://llm.<baseDomain>/key-manager`), then:

```bash
curl https://llm.your-cluster.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4", "messages": [{"role": "user", "content": "Hello"}]}'
```

### Internal access (JWT from JupyterLab or in-cluster service)

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
