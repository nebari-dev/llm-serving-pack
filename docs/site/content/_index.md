+++
title = "LLM Serving Pack"
+++

The **Nebari LLM Serving Pack** turns a Kubernetes cluster into a self-service,
access-controlled LLM inference platform. You declare the models you want to serve as
`LLMModel` custom resources, and the operator handles everything downstream: downloading the
weights, running vLLM serving pods, intelligent inference scheduling, exposing internal and
external endpoints, and enforcing per-model access control.

## Why it exists

Standing up LLM inference for more than one team usually means hand-wiring a different stack for
every model: GPU scheduling, an inference server, ingress and TLS, who is allowed to call it,
API keys for external callers, in-cluster auth for workloads, and rate limiting so one user
cannot starve the rest. Doing that by hand per model is slow, inconsistent, and easy to get
wrong in ways that leak access.

This pack collapses all of that into a single declarative resource. An admin applies one
`LLMModel` CR, and the model comes up served, routed, authenticated, and rate-limited, with
access scoped to the OIDC groups you name.

## What you do

```yaml
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: my-model
spec:
  model:
    name: Qwen/Qwen3-8B
    source: huggingface
  resources:
    gpu: { count: 1, type: nvidia }
  access:
    groups: [data-science]   # only this Keycloak group can call the model
```

Apply it, and the operator downloads the weights, starts vLLM, registers the model with the
inference scheduler, creates the internal and external routes, and gates them on the groups you
listed. See the [Quickstart](/quickstart/) for the end-to-end flow.

## What you get

- **Declarative model serving** - one `LLMModel` CR per model; the operator reconciles the rest.
- **Per-model access control** via OIDC groups - generic for any OIDC provider, tested against
  Keycloak.
- **Two auth paths per model** - external consumers authenticate with API keys; in-cluster
  workloads use JWT bearer tokens.
- **Self-service API keys** - an optional web UI lets users mint and revoke keys for the models
  they are allowed to use.
- **Flexible model loading** - pull from HuggingFace (default) or mount an OCI/modelcar image.
- **Token counting and rate limiting** on both endpoints via Envoy AI Gateway.
- **Intelligent inference scheduling** via llm-d's `InferencePool` and endpoint picker.
- **Network isolation** - model pods are reachable only through the Gateway, enforced by
  NetworkPolicy.

## Who it's for

Platform and infrastructure teams running [Nebari](https://nebari.dev) (or a compatible
Kubernetes cluster) who need to offer LLM inference to multiple data science teams or
applications, with real access control and without rebuilding the serving stack for every model.

## Where to start

- **[Quickstart](/quickstart/)** - deploy the pack and serve your first model.
- **[Installation](/installation/)** - the full production runbook (ArgoCD, GPU operator, Keycloak, AI Gateway).
- **[Configuration](/configuration/)** - Helm values and the `LLMModel` CRD reference.
- **[Architecture](/architecture/)** - how the operator, auth, and gateway fit together.

> **Maturity:** this pack is at **alpha** (`v0.1.0-alpha.x`). See
> [Known Limitations](https://github.com/nebari-dev/llm-serving-pack#known-limitations)
> for current constraints.
