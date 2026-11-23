# Confirmed patterns

### Gateway kinds are handled as unstructured objects
The operator handles Gateway API and Envoy Gateway kinds as `unstructured.Unstructured`. It imports no typed Gateway API or Envoy Gateway module. See `operator/go.mod`, `reconcilers/routing.go`, `reconcilers/passthrough.go` and `unstructured_apply.go`.
**Confirmed:** 2026-11-20 (issue #233)

### Uncached unstructured reads need only the `get` verb
Unstructured reads go to the API server uncached, so reading a kind the operator does not watch needs only `get` RBAC, not `list` or `watch`. See `operator/internal/controller/passthroughmodel_endpoints.go:26-30` and `charts/nebari-llm-serving/templates/operator-clusterrole.yaml:72-77`. This holds as long as `operator/cmd/main.go` does not enable the `Unstructured` cache option on the manager's client.
**Confirmed:** 2026-11-20 (issue #233)
