---
title: Shared Storage
---
Model files are large. Downloading a 30-80 GB model on every pod start is slow, wastes bandwidth, and delays requests. The LLM Serving Pack solves this with persistent model storage managed by the operator: models are downloaded once, cached on a PVC, and shared across replicas.

## How model storage works

When you create an `LLMModel` resource, the operator builds a storage spec that consists of:

- A volume (PVC or emptyDir)
- An init container that populates the volume before vLLM starts
- A volume mount at `/model-cache` shared between the init container and the vLLM container

All model loading paths - HuggingFace and OCI - mount the model at `/model-cache` inside the pod. This is the path passed to vLLM via `--model`.

## HuggingFace (default source)

When `model.source` is `huggingface` (or any value other than `oci`, including an empty string when the field is left unset - the operator treats any non-`oci` source as HuggingFace), the operator:

1. Creates a PVC named `<llmmodel-name>-model-storage` in the same namespace
2. Adds an init container running `ghcr.io/nebari-dev/nebari-llm-serving-pack/model-downloader:latest`
3. The init container runs `hf download <model-name> --local-dir /model-cache`
4. Mounts the PVC at `/model-cache` in both the init container and the vLLM container

The `model-downloader` image is a thin wrapper around the official `huggingface_hub` CLI. It passes all arguments directly to `hf download`.

### Idempotent downloads and crash recovery

`hf download` checksums existing files and only fetches what is missing or corrupted. If a pod dies mid-download, the init container on the next pod resumes from where it left off rather than starting over, because the partially downloaded files remain on the PVC.

### Concurrent replicas

The operator does not implement a cross-replica download lock - there is no `.locked` file or stale-lock takeover. The init container simply runs `hf download` and lets the Hugging Face Hub client manage the cache.

When `serving.replicas > 1` and all replicas share one PVC, each replica runs its own `model-downloader` init container independently. Because `hf download` is idempotent, replicas that start after the first one finishes just checksum the existing files and exit quickly. There is no operator-level coordination between init containers that start at the same time; concurrent-download safety is whatever the Hugging Face Hub client provides for a shared `--local-dir`.

Note that with the default `ReadWriteOnce` access mode (see [PVC access mode](#pvc-access-mode) below), replicas can only share the PVC when scheduled on the same node, so concurrent init containers are confined to one node in that configuration.

### Gated models and HuggingFace tokens

For gated models (e.g. Llama, Devstral), create a Kubernetes Secret with your HuggingFace token and reference it in the `LLMModel`:

```yaml
spec:
  model:
    source: huggingface
    name: "mistralai/Devstral-Small-2505"
    authSecretName: hf-token   # Secret must contain key HF_TOKEN
```

Create the secret:

```bash
kubectl create secret generic hf-token -n <operator-namespace> \
  --from-literal=HF_TOKEN=hf_your_token_here
```

The operator injects `HF_TOKEN` as an environment variable into the init container.

### PVC access mode

The PVC is created with `accessMode: ReadWriteOnce`. Most cloud block storage classes only support `ReadWriteOnce`, which means all replicas must be scheduled on the same node when sharing the PVC. If you need replicas on different nodes, use a storage class backed by a network filesystem (NFS, EFS, Filestore) that supports `ReadWriteMany`.

The PVC access mode is not configurable in the current API - the operator always requests `ReadWriteOnce`. The workaround for multi-node deployments is to use a storage class where `ReadWriteOnce` PVCs are accessible across nodes (e.g. EFS with the EFS CSI driver on AWS).

## OCI/modelcar source

When `model.source` is `oci`, the model weights are baked into a container image. No PVC is created. Instead:

1. The operator creates an `emptyDir` volume (no size limit set)
2. An init container uses `model.image` as its container image and runs `cp -r /models/. /shared-models/`
3. The vLLM container mounts the same `emptyDir` at `/model-cache`

```yaml
spec:
  model:
    source: oci
    name: "my-org/my-model"
    image: "registry.example.com/models/my-model:v1"
```

Because `emptyDir` is ephemeral, the model is copied from the image on every pod start. This is fast for models already cached by the container runtime on the node, but it means every new node must pull the full image.

For reproducibility, use image digests rather than tags:

```yaml
image: "registry.example.com/models/my-model@sha256:abc123..."
```

Tags are mutable; a digest pins the exact image layer set.

Note: Kubernetes image volumes (alpha in 1.31, beta in 1.35) would eliminate the copy step, but many clusters do not have the `ImageVolume` feature gate enabled. The init-container copy path is the only supported OCI mechanism in v0.1.

## Storage type: PVC vs emptyDir for HuggingFace models

For HuggingFace models you can choose between `pvc` (default) and `emptyDir`:

| | `pvc` | `emptyDir` |
|---|---|---|
| Model survives pod restart | Yes | No |
| Model survives node reboot | Yes | No |
| Download on first pod only | Yes | Every restart |
| Storage billed separately | Yes | Node disk |
| Suitable for production | Yes | Small models only |

When `storage.type: emptyDir` and `preload: true`, the validating webhook emits a warning at apply time to alert you that every pod restart will re-download the model.

## Sizing the model store

There is no default storage size - you must set `model.storage.size` explicitly. Size the PVC to hold the full model plus a small buffer for partial downloads and future model revisions.

A rough sizing guide:

- Model size on disk is close to the parameter count in bytes at the precision used. A 7B parameter model at FP16 is ~14 GB; at INT4 quantization it is ~4 GB.
- Check the model's HuggingFace repository for the exact file sizes before setting the PVC size.
- Add 10-20% headroom for checksum and temporary files during download.

Example from the included manifests:

```yaml
# Qwen3.5-35B-A3B INT4 quantized - ~20 GB on disk, PVC sized to 30Gi
storage:
  type: pvc
  size: "30Gi"

# Devstral-Small at full precision - ~60+ GB on disk, PVC sized to 80Gi
storage:
  type: pvc
  size: "80Gi"
```

## Storage class

The operator uses a pack-level default storage class for all model PVCs. Set it in your Helm values:

```yaml
defaults:
  storage:
    storageClassName: ""   # empty = use the cluster's default StorageClass
```

You can override the storage class per model:

```yaml
spec:
  model:
    storage:
      type: pvc
      size: "30Gi"
      storageClassName: efs-sc   # overrides the pack default
```

For clusters on AWS, GCP, or Azure, use a storage class backed by cloud-native network storage (EFS, Filestore, Azure Files) rather than local disk. Network storage is not limited by node disk capacity, survives node replacement, and can be provisioned at any size.

See [Configuration](/configuration/) for the full Helm values reference.

## Disabling preload

By default, the operator adds an init container that downloads the model before vLLM starts (`preload` defaults to `true`). You can disable this if you are using a storage class that pre-populates the PVC externally or if you want vLLM to handle model loading itself:

```yaml
spec:
  model:
    preload: false
```

With `preload: false`, no init container is added and no download step occurs. The PVC (for `storage.type: pvc`) is still created and the volume is still mounted at `/model-cache`; you are responsible for ensuring the model files are present when vLLM starts.

## Architecture context

For more on how the operator manages the full lifecycle of an `LLMModel` resource - from PVC creation through routing configuration - see [Architecture](/architecture/).
