# Getting Started (Local Dev Cluster)

This guide walks through setting up a local development environment using [kind](https://kind.sigs.k8s.io/) to test the nebari-llm-serving-pack without a full Nebari deployment.

## Prerequisites

Install the following tools before proceeding:

- [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) v0.20+
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [helm](https://helm.sh/docs/intro/install/) v3.12+
- [Docker](https://docs.docker.com/get-docker/) (or compatible container runtime)
- [Go](https://go.dev/doc/install) 1.22+ (for building the operator and key manager)

## 1. Clone the repo

```bash
git clone https://github.com/nebari-dev/nebari-llm-serving-pack
cd nebari-llm-serving-pack
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
- Envoy Gateway
- Envoy AI Gateway
- The `LLMModel` CRD
- Test `GatewayClass` and `Gateway` resources

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

This creates an `LLMModel` named `test-model` in the `llm-operator-system` namespace. The operator reconciles it and creates the supporting resources. (Per [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59) all LLMModels must live in the operator's own namespace - the validating webhook rejects anywhere else.)

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
- `SecurityPolicy` resources for auth enforcement (the API-key Secret they reference is co-located in this same namespace; see [#59](https://github.com/nebari-dev/nebari-llm-serving-pack/issues/59) for why)

## 8. Test the key manager API

The key manager exposes an HTTP API for generating and revoking API keys. In the dev cluster, forward its port:

```bash
kubectl -n llm-operator-system port-forward svc/llm-key-manager 8080:8080 &
```

List models (requires a JWT in the `Authorization` header or an identity cookie):

```bash
# With a fake JWT (the dev server accepts any token for testing)
curl -s http://localhost:8080/api/v1/models \
  -H "Authorization: Bearer fake-jwt-token" | jq .
```

Create an API key for the test model:

```bash
curl -s -X POST http://localhost:8080/api/v1/keys \
  -H "Authorization: Bearer fake-jwt-token" \
  -H "Content-Type: application/json" \
  -d '{"modelName": "test-model"}' | jq .
```

The response includes the generated key. Keys are stored as Kubernetes Secrets in the operator namespace (defaults to `llm-operator-system` for the dev cluster, `nebari-llm-serving-system` for the chart):

```bash
kubectl -n llm-operator-system get secrets -l llm.nebari.dev/model
```

## 9. Cleanup

When you are done, delete the kind cluster:

```bash
make teardown
```

Or equivalently:

```bash
make clean
```

## Next steps

- Read [docs/design.md](design.md) for the full architecture and CRD spec
- See `dev/manifests/test-model.yaml` for an annotated example `LLMModel`
- Check the Helm chart at `charts/nebari-llm-serving/` for production deployment values
- For a real deployment with GPUs and OIDC, see the [Quick start](../README.md#quick-start) in the main README
