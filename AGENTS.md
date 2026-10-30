# AGENTS.md

Rules for anyone changing this repo, people and coding agents alike. Reviews
treat a break from a rule here as a blocker, the same as a failing test. If a
rule is wrong for your change, change this file in the same PR and say why.

## Layout

| Path | What it is |
|------|------------|
| `operator/` | The LLMModel operator (Go, kubebuilder). `make test` |
| `key-manager/` | The API key service (Go). `go test ./...` |
| `frontend/` | The key manager SPA (React). `npm test`, `npm run check` |
| `model-downloader/` | The image that pulls model weights into a PVC |
| `charts/nebari-llm-serving/` | The Helm chart that installs the pack |
| `examples/` | Manifests the docs embed and readers copy |
| `docs/` | The docs site (Astro/Starlight). `make docs-test` |
| `dev/` | The local kind cluster and end-to-end scripts |

Before you push a chart or example change, run `make chart-check-examples` and
`make chart-diff-render`. CI runs both.

## Helm chart

**Every namespace comes from the namespace helper.** Each namespaced resource
gets its namespace from `nebari-llm-serving.operatorNamespace`. Never use
`.Release.Namespace` or a hard-coded name. A subchart calls the parent's
helper rather than computing its own: named templates are shared across a
parent and its subcharts, but values are not.

**Whoever creates a namespace deletes it.** When the chart creates the pack's
Namespace, uninstall removes it and everything in it. Never put a keep
annotation on that Namespace. To keep data across an uninstall, the namespace
has to outlive the release: the user creates it and sets
`createNamespace: false`.

**Optional features are off by default and leave the default render
unchanged.** `make chart-diff-render` renders the chart with only its required
values (`scripts/chart-render-values.yaml`) and diffs it against `main`. A
non-empty diff means every existing install changes on upgrade. If that is
intended (a fix, a version bump, a new default), the PR carries the
`default-render-change` label.

**Everything works under `helm template`.** Argo CD installs the pack with
`helm template`, not `helm install`. So:

- no `lookup`, and no `.Release.Revision` or `.Release.IsUpgrade`;
- no message that only `NOTES.txt` carries, because Argo CD never shows it;
- a resource kept on delete carries both `helm.sh/resource-policy: keep` and
  `argocd.argoproj.io/sync-options: Delete=false`.

**One-time setup Jobs are ordinary resources, not hooks.** Name each Job with
a hash of its full rendered pod template, so a change to any part of the pod
gives the Job a new name. A Job's pod template is immutable, and a reused name
fails the upgrade. Don't set `ttlSecondsAfterFinished`: Argo CD would recreate
the Job every time it is cleaned up.

**`fullnameOverride` and `nameOverride` name resources.** They don't choose
the namespace.

## Services

**Fail closed.** A service or UI that shows one user's data to another refuses
to start or render without sign-in. A dev mode that turns sign-in off is a
separate, explicit flag, and the service refuses it when an issuer is also
configured.

**"I can't check right now" is a 503 with `Retry-After`, not a 401.** When
the token validator has no keys yet or can't reach the issuer, the request
gets a 503. A 401 tells the client its credentials are bad, and it stops
retrying.

**New UIs sign in at the gateway.** A new UI uses the NebariApp's gateway
sign-in and reads the user from the gateway's headers. Don't add another
browser-side keycloak-js client. The key manager SPA moves to gateway sign-in
in #222.

**Shared code stays shared.** Token validation and API clients that more than
one module needs live in one shared module that each module imports. Don't
copy them into a module.

## Docs and examples

**A change to a value, default, name or namespace updates the docs in the
same PR.** That means `docs/src/content/docs/configuration.mdx`, every page
that mentions the old behavior, and every example under `examples/`. Search
for the old name and the old default, not only the new one.

**A change that needs action on upgrade gets an entry on the Upgrading page**
(`docs/src/content/docs/upgrading.mdx`, under "Release-specific steps"). A
renamed resource or namespace always needs action: Helm deletes whatever is
missing from the new render.

**Examples render against the chart in the same commit.** `make
chart-check-examples` renders each Argo CD Application's chart values with the
local chart. It checks that the pinned chart version matches `Chart.yaml` and
that `destination.namespace` matches the pack's namespace. A release PR that
bumps `Chart.yaml` bumps the examples' pins with it.
