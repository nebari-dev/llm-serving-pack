# Confirmed patterns

### Gateway kinds are handled as unstructured objects
The operator handles Gateway API and Envoy Gateway kinds as `unstructured.Unstructured`. It imports no typed Gateway API or Envoy Gateway module. See `operator/go.mod`, `reconcilers/routing.go`, `reconcilers/passthrough.go` and `unstructured_apply.go`.
**Confirmed:** 2026-11-20 (issue #233)

### Uncached unstructured reads need only the `get` verb
Unstructured reads go to the API server uncached, so reading a kind the operator does not watch needs only `get` RBAC, not `list` or `watch`. See `operator/internal/controller/passthroughmodel_endpoints.go:26-30` and `charts/nebari-llm-serving/templates/operator-clusterrole.yaml:72-77`. This holds as long as `operator/cmd/main.go` does not enable the `Unstructured` cache option on the manager's client.
**Confirmed:** 2026-11-20 (issue #233)

### Every dev tool pins `kubectl` to `--context kind-<CLUSTER_NAME>`
Dev scripts and make targets pass `--context kind-<CLUSTER_NAME>` to `kubectl` and never use the current context, so a developer's other clusters are not touched. See `dev/Makefile:12-16`, `dev/test-auth.sh:31-33` and `dev/usage-e2e.sh:42-44`.
**Confirmed:** 2026-11-25 (spec: docs/superpowers/specs/2026-11-24-ci-journey-tests-design.md)

### Dev make targets are the entry points for local cluster work
Each target in `dev/Makefile` has a `##` help comment, and local cluster work starts from a target rather than a raw script. See `dev/Makefile:37-177`.
**Confirmed:** 2026-11-25 (spec: docs/superpowers/specs/2026-11-24-ci-journey-tests-design.md)

### Repo tooling written in Go lives under `operator/cmd/<tool>/`
It is run with `go run ./cmd/<tool>` from `operator/`. See `operator/cmd/check-default-images/main.go` and `.github/workflows/test.yaml:36-38`.
**Confirmed:** 2026-11-25 (spec: docs/superpowers/specs/2026-11-24-ci-journey-tests-design.md)

### Docs pages embed `examples/*.yaml` through a `?raw` import
Pages import the file with `?raw` and show it with `ExampleCode.astro`, with no inline copy of the manifest. See `docs/test/docs-examples.test.mjs:36-48` and `docs/src/content/docs/architecture.mdx:5-6`.
**Confirmed:** 2026-11-25 (spec: docs/superpowers/specs/2026-11-24-ci-journey-tests-design.md)

### Markdown transforms for the docs are remark plugins
They live in `docs/src/plugins/` and are unit-tested with vitest in `docs/tests/`. See `docs/astro.config.mjs:66`, `docs/src/plugins/remark-base-links.ts` and `docs/tests/remark-base-links.test.ts`.
**Confirmed:** 2026-11-25 (spec: docs/superpowers/specs/2026-11-24-ci-journey-tests-design.md)

### A session probes a shared prerequisite once
When many tests depend on one prerequisite, the session checks it once and fails every dependent test with a single precise diagnosis instead of many unrelated errors. The model is NIC `journeys/conftest.py:58-87` (`gateway_reachable`).
**Confirmed:** 2026-11-25 (spec: docs/superpowers/specs/2026-11-24-ci-journey-tests-design.md)
