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
  provisioned out-of-band. The pack does not ship one; the operator
  patches its own HTTPS listeners for `llm.<baseDomain>` and
  `llm-internal.<baseDomain>` onto the external and internal Gateways.
  Pre-existing listeners (base-domain, Argo CD, Keycloak, etc.) are
  matched by name and left alone. The operator also creates the
  `Certificate` for those two hostnames in its own namespace, using
  the cluster-issuer named by `platform.tls.clusterIssuer` (default
  `letsencrypt-production`); the chart does not template the
  Certificate separately. Set `platform.gateway.manageSharedListeners:
  false` if a cluster admin owns the listeners out-of-band - the
  operator will still create the Certificate so admins can wire the
  resulting Secret in by hand. Note: flipping `manageSharedListeners`
  from true to false does not retroactively remove listeners the
  operator already added. Remove them by hand before flipping the flag
  if a clean slate is required.

- **Envoy Gateway must be installed with the AI Gateway extension manager
  wired up.** Without this, the routing layer does not work at runtime
  (requests 404 with "No matching route found"). See the next section
  for the exact Helm values.

The `llmd-test` cluster wires all of this up via ArgoCD apps in
`clusters/llmd-test/apps/` of the `nic-test` repo - use those as the
reference shape.

## Wiring Envoy Gateway to the AI Gateway extension manager

The pack's per-model routing uses the Envoy AI Gateway's `ext_proc`
filter to extract the `model` field from OpenAI-style request bodies
into an `x-ai-eg-model` header that HTTPRoute matchers then dispatch
on. For that filter to be programmed into Envoy's filter chain, three
things need to be true on the cluster:

1. `envoy-gateway` is configured to call AI Gateway's extension server
   at reconcile time.
2. `envoy-gateway` is told that `inference.networking.k8s.io/InferencePool`
   is a valid backend kind (otherwise the model's HTTPRoute falls back
   to `direct_response: 500`).
3. The AI Gateway admission webhook injects an `ai-gateway-extproc`
   sidecar into the Envoy proxy pod (runs automatically once
   AI Gateway is installed and an AIGatewayRoute exists on the
   Gateway; no extra config needed).

Items 1 and 2 require specific `envoy-gateway` Helm values. Match the
canonical shape from [`envoyproxy/ai-gateway` `envoy-gateway-values.yaml`](https://github.com/envoyproxy/ai-gateway/blob/v0.5.0/manifests/envoy-gateway-values.yaml):

```yaml
config:
  envoyGateway:
    gateway:
      controllerName: gateway.envoyproxy.io/gatewayclass-controller
    extensionApis:
      enableEnvoyPatchPolicy: true
      enableBackend: true                  # required for AI Gateway
    extensionManager:
      hooks:
        xdsTranslator:
          translation:
            listener:   { includeAll: true }
            route:      { includeAll: true }
            cluster:    { includeAll: true }
            secret:     { includeAll: true }
          post:
            - Translation
            - Cluster
            - Route
      service:
        fqdn:
          hostname: ai-gateway-controller.envoy-ai-gateway-system.svc.cluster.local
          port: 1063
      backendResources:
        - group: inference.networking.k8s.io
          kind: InferencePool
          version: v1
```

If these are missing on an existing install, add them, restart the
`envoy-gateway` Deployment, and recreate the Envoy proxy pod (so the
admission webhook injects the `ai-gateway-extproc` sidecar):

```bash
kubectl rollout restart -n envoy-gateway-system deployment/envoy-gateway
# After rollout:
kubectl delete pod -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=<your-gateway>
```

Verify by checking the Envoy admin `/config_dump` for the model's SNI
filter chain - it should include `envoy.filters.http.ext_proc/aigateway`
alongside `api_key_auth` (external) or `jwt_authn` + `rbac` (internal).

## Component layout

The pack expects the following to be installed on the cluster, in
roughly this order. Every one of these is its own ArgoCD app on
`llmd-test`; sync-waves are the third column.

| Component | Purpose | Sync-wave |
|---|---|---|
| cert-manager | Issues TLS certs for the gateway, webhook, NebariApps | 1 |
| envoy-gateway | The Gateway API data-plane. **Must be installed with the `extensionApis` + `extensionManager` + `backendResources` config from the section above.** | 2 |
| nvidia-gpu-operator | GPU device plugin + drivers on GPU nodes | 2 |
| gateway-config | The shared `Gateway` + `GatewayClass` resources | 2 |
| cluster-issuers | cert-manager `ClusterIssuer` (Let's Encrypt etc.) | 3 |
| envoy-ai-gateway-crds | Envoy AI Gateway CRDs (must precede the controller) | 3 |
| certificates | TLS Certificates for pre-existing shared listeners (base-domain, Argo CD, Keycloak). The `llm.<base>` + `llm-internal.<base>` Certificate is operator-owned (sync-wave 7). | 3 |
| httproutes | Cluster-level HTTPRoutes for shared services (argocd, keycloak) | 3 |
| envoy-ai-gateway | Envoy AI Gateway controller. Its `pod-mutator` admission webhook injects the `ai-gateway-extproc` sidecar into Envoy proxy pods created AFTER this controller comes up. | 4 |
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
              manageSharedListeners: true
            tls:
              # ClusterIssuer the operator will use to issue the shared
              # llm.<baseDomain> + llm-internal.<baseDomain> Certificate.
              # Must already exist on the cluster. Default in the chart
              # is "letsencrypt-production"; override here if your
              # cluster uses a different name.
              clusterIssuer: letsencrypt-issuer

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
- Pin a tagged release (`v0.1.0-alpha.7` or later). The chart's image
  tags default to `.Chart.AppVersion` so the chart and the images move
  together. Do not leave `targetRevision: HEAD` in production.
- `auth.oidc.issuerURL` must be the **public** (browser-reachable)
  Keycloak issuer URL. Envoy's JWT filter resolves the JWKS endpoint
  from this. Currently this assumes Keycloak (path is hardcoded to
  `/protocol/openid-connect/certs`); see issue #66 for the
  provider-agnostic discovery refactor.
- `platform.tls.clusterIssuer` must name a cluster-scoped cert-manager
  `ClusterIssuer` that's already installed. HTTP-01 is the assumed
  challenge type; wildcards are not supported (and not required - the
  operator issues a cert with the two shared hostnames as explicit
  SANs). HTTP-01 needs DNS for both hostnames to resolve to the
  Gateway LB before the install; a wildcard CNAME at the base domain
  covers this naturally.

## Per-model LLMModel CR

LLMModels must live in the operator's namespace. A minimal CR for a
real GPU model:

```yaml
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: qwen3-5-35b-a3b-gptq-int4
  namespace: nebari-llm-serving-system
spec:
  model:
    name: "Qwen/Qwen3.5-35B-A3B-GPTQ-Int4"    # also the "model" field clients send
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
    external: { enabled: true }
    internal: { enabled: true }
```

All models on the cluster share the same hostname pair:

- External (API key): `https://llm.<baseDomain>/v1/chat/completions`
- Internal (JWT): `https://llm-internal.<baseDomain>/v1/chat/completions`

Clients dispatch between models by setting the `model` field in the
OpenAI-style request body (matches `spec.model.name` above). Example:

```bash
curl -sS -X POST https://llm.<baseDomain>/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"Qwen/Qwen3.5-35B-A3B-GPTQ-Int4","messages":[{"role":"user","content":"hi"}],"max_tokens":10}'
```

> `endpoints.external.subdomain` on the CRD is currently unused at the
> routing layer but kept for forward compatibility if wildcard certs
> (DNS-01) become available. Leave it empty unless you have a specific
> reason to set it.

## Validation checklist

After ArgoCD reports the `nebari-llm-serving` app `Synced` and `Healthy`:

1. Operator + key-manager pods are Ready in the operator namespace.
2. `kubectl get llmmodel -A` shows your model with `READY=True`.
3. The model's pod is `Running` and the vLLM container reports
   "Started server" in its logs.
4. `kubectl get certificate -n nebari-llm-serving-system nebari-llm-shared-tls`
   reports `READY=True` (cert-manager solved HTTP-01 for both shared
   hostnames; if this stays `False`, DNS isn't resolving yet).
5. The shared Gateway has `llm-https` and `llm-internal-https`
   listeners alongside any pre-existing listeners. Verify with:
   `kubectl get gateway <name> -n envoy-gateway-system -o jsonpath='{range .spec.listeners[*]}{.name}: {.hostname}{"\n"}{end}'`.
6. The model's `SecurityPolicy`s (one external, one internal) report
   `Accepted=True`.
7. The Envoy proxy pod has an `ai-gateway-extproc` init container
   (native sidecar) and the filter chain for the `llm.<baseDomain>`
   SNI contains `envoy.filters.http.ext_proc/aigateway` (check via
   port-forward on the envoy admin port `19000` and
   `/config_dump`). If the `ext_proc` filter is missing, the
   envoy-gateway `extensionManager` config is wrong or the
   `ai-gateway-extproc` container wasn't injected on pod create -
   restart the Envoy proxy pod.
8. As a member of the model's group, log into the key-manager UI at
   `https://llm-keys.<baseDomain>`, mint an API key, and call the
   external endpoint with it (see `curl` example above). Expect a
   real chat-completion response, not a 5xx.
9. From a JupyterLab pod (or any in-cluster service with a JWT for the
   same group), call the internal endpoint with the JWT and confirm
   you get a real model response (not a 500 `direct_response`).

If any of those fail, check the operator and key-manager logs first
(`kubectl -n nebari-llm-serving-system logs ...`), then the Envoy
access logs (`kubectl -n envoy-gateway-system logs <envoy-pod>
-c envoy | grep POST`). `route_name` in the access log tells you
which rule matched; `rule/0` means the per-model rule, `rule/1` means
the AI Gateway catch-all (model not recognised).

## Known limitations

- **Keycloak only (path conventions).** The operator constructs the
  JWT JWKS URL using the Keycloak path convention. Other OIDC
  providers will 404. See #66 (provider-agnostic discovery).
- **No API key rotation or expiry yet.** See #67.
- **No metrics endpoint.** Logs only. See #69.
- **Gateway not provisioned by this chart.** You must install the
  shared Gateway out-of-band. The pack's operator patches its own
  HTTPS listeners onto it at runtime; the Gateway resource itself is
  not pack-owned. See the `gateway-config` ArgoCD app on `llmd-test`
  for a reference.
- **OCI / modelcar model source is untested in production.** The code
  path exists but has not been exercised end-to-end on a real
  cluster. If you need it, validate first on a dev cluster.
- **Helm chart install is lint-validated only in CI.** Real install
  in CI is tracked in #68.
- **Single-GPU clusters can deadlock on rolling updates.** The
  Deployment uses `RollingUpdate` with `maxUnavailable: 25%`, so a
  new pod can't schedule while the old one still holds the only GPU.
  If you hit this, `kubectl delete pod` on the old one; the new pod
  then schedules.

## Reference: `llmd-test` ArgoCD apps

For a complete worked example covering every prerequisite at a known
working version, see
[`clusters/llmd-test/apps/`](https://github.com/dcmcand/nic-test/tree/main/clusters/llmd-test/apps)
in the `nic-test` repo.
