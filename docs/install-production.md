# Production Install (ArgoCD + Nebari)

This runbook covers installing the LLM serving pack into a Nebari cluster
managed by ArgoCD. For local development against `kind` see
[`getting-started.md`](getting-started.md) instead.

> **Status:** this runbook tracks the install path validated against the
> `llmd-test` reference cluster. It is the source of truth for what must
> be true on a fresh Nebari deployment for the pack to come up cleanly.
> Drift here from `llmd-test`'s ArgoCD apps is a bug in this doc.

## What you need before you start

- A Nebari cluster (any cloud) with at least one GPU node group available.
- ArgoCD running and reachable, with permission to create
  `Application` resources in the `argocd` namespace.
- A working DNS zone you can put records in. The pack will mint
  cert-manager Certificates against this domain.
- Keycloak deployed and reachable, with a realm configured. Users land
  in groups via the standard OIDC `groups` claim. The realm's admin
  credentials must live in a Kubernetes Secret reachable by the
  `nebari-operator` (the operator looks up users in Keycloak to enforce
  group-based access).
- The Nebari shared Gateway (`gateway.networking.k8s.io/Gateway`) is
  provisioned out-of-band. The pack does not ship one, but starting at
  v0.1.0-alpha.3 the operator patches its own HTTPS listeners for
  `llm.<baseDomain>` and `llm-internal.<baseDomain>` onto the external
  and internal Gateways. Pre-existing listeners on those Gateways
  (base-domain, Argo CD, Keycloak, etc.) are matched by name and left
  alone. The operator also creates the `Certificate` for those two
  hostnames in its own namespace, using the cluster-issuer named by
  `platform.tls.clusterIssuer` (default `letsencrypt-production`);
  the chart does not template the Certificate separately. Set
  `platform.gateway.manageSharedListeners: false` if a cluster admin
  owns the listeners out-of-band - the operator will still create the
  Certificate so admins can wire the resulting Secret in by hand.

The `llmd-test` cluster wires all of this up via ArgoCD apps in
`clusters/llmd-test/apps/` of the `nic-test` repo - use those as the
reference shape.

## Component layout

The pack expects the following to be installed on the cluster, in
roughly this order. Every one of these is its own ArgoCD app on
`llmd-test`; sync-waves are the third column.

| Component | Purpose | Sync-wave |
|---|---|---|
| cert-manager | Issues TLS certs for the gateway, webhook, NebariApps | 1 |
| envoy-gateway | The Gateway API data-plane | 2 |
| nvidia-gpu-operator | GPU device plugin + drivers on GPU nodes | 2 |
| gateway-config | The shared `Gateway` + `GatewayClass` resources | 2 |
| cluster-issuers | cert-manager `ClusterIssuer` (Let's Encrypt etc.) | 3 |
| envoy-ai-gateway-crds | Envoy AI Gateway CRDs (must precede the controller) | 3 |
| certificates | The shared gateway TLS Certificate for pre-existing listeners (base-domain, Argo CD, Keycloak). Pack-owned `llm.*` listeners get their Certificate from the operator at sync-wave 7. | 3 |
| httproutes | Cluster-level HTTPRoutes for shared services (argocd, keycloak) | 3 |
| envoy-ai-gateway | Envoy AI Gateway controller | 4 |
| gateway-api-inference-extension | InferencePool / InferenceModel CRDs | 4 |
| keycloak | OIDC provider | 4 |
| postgresql | Backing store for keycloak | 4 |
| opentelemetry-collector | Telemetry sink | 4 |
| nebari-operator | Provisions NebariApps (HTTPRoute + cert + Keycloak client + landing-page tile) | 5 |
| nebari-landingpage | The Nebari landing page itself | 6 |
| **nebari-llm-serving** (this pack) | The operator + key-manager + LLMModel CRD | 7 |
| LLMModel CRs | Per-model `LLMModel` resources | 7 (or later) |

ArgoCD enforces the order via `argocd.argoproj.io/sync-wave` annotations.

## Required nebari-operator config

The pack's key-manager UI is exposed as a `NebariApp`. The
`nebari-operator` needs Keycloak admin credentials and the cluster's
TLS issuer name to provision the OIDC client and the Certificate. Set
these on the operator deployment:

```yaml
env:
  - name: KEYCLOAK_ENABLED
    value: "true"
  - name: KEYCLOAK_URL
    value: "http://keycloak-keycloakx-http.keycloak.svc.cluster.local:8080"
  - name: KEYCLOAK_REALM
    value: "nebari"
  - name: KEYCLOAK_ADMIN_SECRET_NAME
    value: "keycloak-admin-credentials"
  - name: KEYCLOAK_ADMIN_SECRET_NAMESPACE
    value: "keycloak"
  - name: KEYCLOAK_EXTERNAL_URL
    value: "https://keycloak.<your-base-domain>"
  - name: TLS_CLUSTER_ISSUER_NAME
    value: "letsencrypt-issuer"
  - name: KEYCLOAK_ISSUER_CONTEXT_PATH
    value: ""
```

The exact secret name and Keycloak service name depend on how you
installed Keycloak. The values above match the `bitnami/keycloak`-style
chart used on `llmd-test`.

## ArgoCD application for this pack

The pack ships as a Helm chart at
`https://github.com/nebari-dev/nebari-llm-serving-pack`,
`charts/nebari-llm-serving`. A minimal ArgoCD `Application`:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: nebari-llm-serving
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "7"
spec:
  project: foundational
  sources:
    - repoURL: https://github.com/nebari-dev/nebari-llm-serving-pack.git
      targetRevision: v0.1.0-alpha.2
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
              storageClassName: efs-sc       # match your cluster's RWX storage class

          auth:
            oidc:
              issuerURL: "https://keycloak.your-cluster.example.com/realms/nebari"
              groupsClaim: groups

          keyManager:
            enabled: true
            nebariApp:
              enabled: true
              hostname: "llm-keys.your-cluster.example.com"
              gateway: public
    # Source 2 (optional): your LLMModel CRs
    - repoURL: https://github.com/your-org/your-cluster-config.git
      targetRevision: main
      path: clusters/<name>/manifests/llm-models
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
```

Notes:
- `CreateNamespace=true` is OK because the chart also ships a
  `Namespace` template that adds the `nebari.dev/managed=true` label
  required by the validating webhook. Helm and ArgoCD both create
  the namespace; the chart-managed labels win.
- Pin a tagged release (`v0.1.0-alpha.2` or later). The chart's
  default image tag (`:latest`) tracks main and can lag behind; pin
  the chart and the images move together.
- `auth.oidc.issuerURL` must be the **public** (browser-reachable)
  Keycloak issuer URL. Envoy's JWT filter resolves the JWKS endpoint
  from this. Currently this assumes Keycloak (path is hardcoded to
  `/protocol/openid-connect/certs`); see issue #61 / #66 for the
  provider-agnostic discovery refactor.

## Per-model LLMModel CR

LLMModels must live in the operator's namespace (post-#59 webhook
constraint). A minimal CR for a real GPU model:

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
  resources:
    gpu:
      count: 1
      type: nvidia
    requests: { cpu: "2", memory: "8Gi" }
    limits:   { cpu: "4", memory: "12Gi" }
  serving:
    replicas: 1
    tensorParallelism: 1
    vllmArgs:
      - "--quantization"
      - "gptq_marlin"
      - "--dtype"
      - "float16"
      - "--max-model-len"
      - "8192"
  access:
    public: false
    groups: ["llm"]
  endpoints:
    external: { enabled: true, subdomain: qwen3-5-35b }
    internal: { enabled: true }
```

The model is then reachable at:
- External (API key): `https://qwen3-5-35b.llm.<baseDomain>`
- Internal (JWT): `https://qwen3-5-35b.llm-internal.<baseDomain>`

## Validation checklist

After ArgoCD reports the `nebari-llm-serving` app `Synced` and `Healthy`:

1. Operator + key-manager pods are Ready in the operator namespace.
2. `kubectl get llmmodel -A` shows your model with `READY=True`.
3. The model's pod is `Running` and the vLLM container reports
   "Started server" in its logs.
4. The model's `SecurityPolicy`s (one external, one internal) report
   `Accepted=True`.
5. As a member of the model's group, log into the key-manager UI at
   `https://llm-keys.<baseDomain>`, mint an API key, and call the
   external endpoint with it.
6. From a JupyterLab pod (or any in-cluster service with a JWT for the
   same group), call the internal endpoint with the JWT and confirm
   you get a real model response (not a 500 `direct_response`).

If any of those fail, check the operator logs and the key-manager
logs first (`kubectl -n nebari-llm-serving-system logs ...`). After
issue #57 every 500-returning code path in the key-manager emits a
structured log line - grep for the failing handler name.

## Known limitations

- **Keycloak only.** The operator constructs the JWT JWKS URL using
  the Keycloak path convention. Other OIDC providers will 404. See
  #61 (fix shipped) and #66 (provider-agnostic discovery).
- **No API key rotation or expiry yet.** See #67.
- **No metrics endpoint.** Logs only. See #69.
- **Gateway not provisioned by this chart.** You must install the
  shared Gateway out-of-band (see the `gateway-config` ArgoCD app on
  `llmd-test` for a reference).
- **Helm chart install is lint-validated only.** Real install in CI
  is tracked in #68. Until that ships, expect to discover install
  issues during the validation checklist above.

## Reference: `llmd-test` ArgoCD apps

For a complete worked example covering every prerequisite at a known
working version, see
[`clusters/llmd-test/apps/`](https://github.com/dcmcand/nic-test/tree/main/clusters/llmd-test/apps)
in the `nic-test` repo.
