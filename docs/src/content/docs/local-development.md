---
title: Local Development
---
This guide walks through setting up a local development environment using [kind](https://kind.sigs.k8s.io/) to test the nebari-llm-serving-pack without a full Nebari deployment.

> **Scope of this dev path.** The local `kind` setup exercises the
> operator, key manager, CRD, webhook, and reconciler logic against a
> real Kubernetes API, with Envoy Gateway and the Envoy AI Gateway
> `ext_proc` extension wired up. Served `LLMModel`s use a mock vLLM
> instead of real model-serving pods, so there is no GPU inference.
> External-provider `PassthroughModel`s route to the real provider, so
> end-to-end inference works against, for example, OpenRouter (see
> "Test an external provider" below). For a full GPU serving deployment
> use a real cluster as documented in the [Installation](/installation/)
> guide.

## Prerequisites

Install the following tools before proceeding:

- [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) v0.20+
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [helm](https://helm.sh/docs/intro/install/) v3.12+
- [Docker](https://docs.docker.com/get-docker/) (or compatible container runtime)
- [Go](https://go.dev/doc/install) 1.25+ (the key manager requires 1.25; the operator requires 1.24 - each builds with the version pinned in its own `go.mod`)

## 1. Clone the repo

```bash
git clone https://github.com/nebari-dev/llm-serving-pack
cd llm-serving-pack
```

## 2. Create the dev cluster

The `dev/Makefile` automates cluster creation and dependency installation:

```bash
cd dev
make setup
```

This creates a kind cluster named `llm-serving-test` and installs:

- cert-manager (for webhook TLS)
- Gateway API CRDs
- Gateway API Inference Extension CRDs
- Envoy AI Gateway
- Envoy Gateway, wired with the AI Gateway `ext_proc` extension (`dev/eg-extension-values.yaml`)
- The `LLMModel` and `PassthroughModel` CRDs
- Test `GatewayClass` and `Gateway` resources

Dependency versions are pinned at the top of `dev/Makefile`. They move as a
set: Envoy AI Gateway v0.5.x requires Envoy Gateway v1.6.x and Gateway API
v1.4.0 (see the [compatibility matrix](https://aigateway.envoyproxy.io/docs/compatibility/)).

The setup takes a few minutes. You can watch progress in the terminal output.

## 3. Build and load images

Build the operator, key manager, and mock vLLM images, then load them into the kind cluster:

```bash
make build-images
make load-images
```

The mock vLLM image simulates a vLLM server for testing without a GPU. It responds to OpenAI-compatible API calls with canned responses.

## 4. Deploy the operator and key manager

```bash
make deploy
```

This applies the manifests in `dev/manifests/` and waits for the deployments to become ready. You can also deploy them individually:

```bash
make deploy-operator     # operator only
make deploy-key-manager  # key manager only
```

Verify the deployments:

```bash
kubectl -n llm-operator-system get pods
```

Expected output:
```
NAME                               READY   STATUS    RESTARTS   AGE
llm-key-manager-xxxxxxxxx-xxxxx    1/1     Running   0          30s
llm-operator-xxxxxxxxx-xxxxx       1/1     Running   0          45s
```

## 5. Deploy a test model

Apply the test `LLMModel` resource, which uses the mock vLLM image:

```bash
make apply-test-model
```

This creates an `LLMModel` named `test-model` in the `llm-operator-system` namespace. The operator reconciles it and creates the supporting resources. Per [#59](https://github.com/nebari-dev/llm-serving-pack/issues/59) all LLMModels must live in the operator's own namespace - the validating webhook rejects anywhere else.

## 6. Watch reconciliation

Watch the `LLMModel` status update as the operator reconciles:

```bash
kubectl -n llm-operator-system get llmmodels -w
```

You should see the `READY` column transition through states as each sub-resource is created. Once all reconcilers complete, the model shows `Ready`.

Check the operator logs if anything looks stuck:

```bash
make logs-operator
```

## 7. Verify resources

Once the model is ready, verify the created resources:

```bash
kubectl -n llm-operator-system get all
kubectl -n llm-operator-system get aigatewayroutes
kubectl -n llm-operator-system get securitypolicies
```

The operator creates:

- A `Deployment` running the mock vLLM pod
- A `Service` for the deployment
- An `InferencePool` for intelligent request scheduling
- `AIGatewayRoute` resources for external (API key) and internal (JWT) access
- `SecurityPolicy` resources for auth enforcement (the API-key Secret they reference is co-located in this same namespace; see [#59](https://github.com/nebari-dev/llm-serving-pack/issues/59) for why)

## 8. Test the key manager API

The key manager exposes an HTTP API for generating and revoking API keys. In the dev cluster, forward its port:

```bash
kubectl -n llm-operator-system port-forward svc/llm-key-manager 8080:8080 &
```

On the dev cluster the key-manager runs with `LLM_DEV_MODE=true`, which
bypasses auth and injects a fixed identity (user `dev`, group `llm`). No token
is required - list models directly:

```bash
curl -s http://localhost:8080/api/models | jq .
```

Create an API key for the test model:

```bash
curl -s -X POST http://localhost:8080/api/keys \
  -H "Content-Type: application/json" \
  -d '{"modelName": "test-model"}' | jq .
```

Outside dev mode the key-manager validates a real Keycloak bearer token on
every `/api` call (RSA signature + `exp` + `iss`), which the SPA obtains via
PKCE.

The response includes the generated key. Keys are stored as Kubernetes Secrets in the operator namespace (defaults to `llm-operator-system` for the dev cluster, `nebari-llm-serving-system` for the chart):

```bash
kubectl -n llm-operator-system get secrets -l llm.nebari.dev/model
```

## 9. Test an external provider (PassthroughModel)

A `PassthroughModel` routes the shared endpoints to an external OpenAI-compatible
provider rather than a locally served model. This path runs end to end on kind
because the provider does the inference. These steps use OpenRouter and assume a
real OpenRouter API key.

Create the provider credential and apply the example model:

```bash
make create-openrouter-secret OPENROUTER_API_KEY=sk-or-v1-...
make apply-passthrough-model
kubectl -n llm-operator-system get passthroughmodel openrouter -w
```

Once it reports `Ready`, reach it through the gateway. The external endpoint
(`llm.local`) uses API-key auth, so inject a client key into the api-keys Secret
to skip the key-manager:

```bash
kubectl -n llm-operator-system patch secret openrouter-api-keys --type merge \
  -p '{"stringData":{"localtester":"sk-localtest-abc123"}}'

SVC=$(kubectl -n envoy-gateway-system get svc \
  -l gateway.envoyproxy.io/owning-gateway-name=nebari-gateway -o name | head -1)
kubectl -n envoy-gateway-system port-forward "$SVC" 8443:443 &

curl -k https://llm.local:8443/v1/chat/completions \
  --resolve llm.local:8443:127.0.0.1 \
  -H "Authorization: Bearer sk-localtest-abc123" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

A curl issued immediately after the Secret patch can return 403: the key
authenticates as soon as Envoy Gateway syncs the Secret, but authorization
waits for the operator to re-render the model's SecurityPolicy allow-list
(typically a few seconds). Retry on a 403 before digging deeper.

The internal endpoint (`llm-internal.local`) always requires a real Keycloak
JWT, even when access is public, so it is not reachable on a bare kind cluster.

## 10. Open the key-manager UI (dev mode)

The UI is a React + TypeScript SPA in `frontend/`, served in production by its
own nginx image. For local work, run it with the Vite dev server against the
dev-mode key-manager - one command wires up the cluster, models, port-forward,
and hot reload:

```bash
./run-dev.sh   # from dev/  ·  also: make run-dev  ·  make ui (repo root)
```

This port-forwards the key-manager API to `http://localhost:8080` and starts the
Vite dev server at `http://localhost:5173` (develop there). Two things make login
disappear locally:

- The dev server sets `VITE_DEV_NO_AUTH=true`, so the SPA skips the keycloak-js
  PKCE redirect.
- The deployed key-manager runs with `LLM_DEV_MODE=true`, which bypasses auth
  and injects a fixed identity (user `dev`, group `llm`).

The UI loads without a login and can mint and revoke keys for any model the
`dev` identity's groups grant access to. Dev mode is controlled by
`LLM_DEV_MODE` on the key-manager Deployment; it is off by default and must
never be enabled in a real deployment.

> **Working on the UI itself?** See [UI Development](/ui-development/) for the
> full hot-reload loop and the frontend quality gate.

## 11. Tail logs

You can tail logs from either component while working:

```bash
make logs-operator
make logs-key-manager
```

## 12. Run unit tests

Run the operator and key manager tests directly without a cluster:

```bash
cd operator && make test
cd key-manager && go test ./...
```

## 13. Cleanup

When you are done, delete the kind cluster:

```bash
make teardown
```

Or equivalently:

```bash
make clean
```

## Next steps

- Read the [Architecture](/architecture/) page for the full design and CRD spec
- See `dev/manifests/test-model.yaml` for an annotated example `LLMModel`
- Check the Helm chart at `charts/nebari-llm-serving/` for production deployment values
- For a real deployment with GPUs and OIDC, see the [Quick start](/quickstart/)
