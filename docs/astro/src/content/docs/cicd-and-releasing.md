---
title: CI/CD and Releasing
---
This page documents every GitHub Actions workflow in the repository, the container images they produce, and the manual release process used to cut new versions. It is aimed at contributors and maintainers.

## Workflows

The repository has six workflows located in `.github/workflows/`.

### Test (`test.yaml`)

**Triggers:** push to `main`, pull requests targeting `main`.

Runs two parallel jobs:

| Job | Component | Command |
|-----|-----------|---------|
| `test-operator` | `operator/` | `make test` |
| `test-key-manager` | `key-manager/` | `go test ./...` |

Both jobs set up Go from the component's own `go.mod`/`go.sum`, so each component pins its Go version independently.

### Lint (`lint.yaml`)

**Triggers:** push to `main`, pull requests targeting `main`.

Runs three parallel jobs:

| Job | Component | Tool |
|-----|-----------|------|
| `lint-operator` | `operator/` | `golangci-lint` v2.4.0 |
| `lint-key-manager` | `key-manager/` | `golangci-lint` v2.4.0 |
| `lint-helm` | `charts/nebari-llm-serving/` | `helm lint` |

The Helm lint job runs `helm lint charts/nebari-llm-serving/` against the chart without any value overrides, so `platform.baseDomain` defaults to empty and any chart template that gates on it must handle the empty case.

### Build Images (`build-images.yaml`)

**Triggers:** push to `main`, push of any `v*` tag, manual `workflow_dispatch`.

Builds and pushes three images to GitHub Container Registry (`ghcr.io`) under the prefix `ghcr.io/nebari-dev/nebari-llm-serving-pack`:

| Image | Build context | Dockerfile |
|-------|--------------|------------|
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/operator` | `operator/` | `operator/Dockerfile` |
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/model-downloader` | `model-downloader/` | `model-downloader/Dockerfile` |
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/key-manager` | `.` (repo root) | `key-manager/Dockerfile` |

Each job uses `docker/metadata-action` to derive tags automatically:

| Condition | Tag applied |
|-----------|------------|
| Any push | `sha-<short SHA>` |
| Push to `main` branch | branch name (`main`) and `:latest` |
| Push of a `v*` tag | semantic version from the tag, `v` prefix preserved (e.g. `v0.1.0-alpha.9`) |

The `latest` tag only applies when building from the default branch. Version tags applied on a `v*` push take the form `v{{version}}` as extracted by `docker/metadata-action`'s semver pattern.

Authentication to GHCR uses the workflow's automatic `GITHUB_TOKEN` with `packages: write` permission.

The chart's `values.yaml` does not set a default tag for the operator and key-manager images; it leaves `tag: ""` and falls back to `.Chart.AppVersion` at render time. This means `helm install` without a tag override pulls whatever image version matches the chart's `appVersion`.

### Docs (`docs.yml`)

**Triggers:** push to `main` and pull requests touching `docs/astro/**`, `scripts/check-links.sh`, or the workflow file itself; manual `workflow_dispatch`.

A single **`docs`** job on Node 22 (npm cache keyed on `docs/astro/package-lock.json`): runs `npm ci`, installs Playwright Chromium (needed to render Mermaid diagrams to SVG at build time), runs `npm test`, then computes the correct `site` and `base` values (production: `https://packs.nebari.dev` + `/llm-serving-pack/`; preview: `https://<alias>.llm-serving-pack.pages.dev` + `/`). It then runs `npm run build` and validates internal links by running `scripts/check-links.sh` (with `SKIP_BUILD=1`) against the built `docs/astro/dist`.

For non-fork PRs and pushes to `main`, the job deploys to Cloudflare Pages via `cloudflare/wrangler-action`. On pull requests from the same repo, a sticky comment is posted with the preview URL (`pages-deployment-alias-url`). Fork PRs run the build and link-check but skip deploy (secrets are unavailable).

### Docs preview cleanup (`docs-preview-cleanup.yml`)

**Triggers:** a pull request closing (merged or not).

When a PR closes, this workflow deletes the Cloudflare Pages **preview** deployments for that PR's branch (via the Pages API, matched by branch and filtered to the preview environment). Direct Upload deploys are not tied to the git branch lifecycle, so without this, preview deployments would linger after the branch is gone. Fork PRs are skipped (no preview, no secrets).

### Add to Project (`add-to-project.yaml`)

**Triggers:** new issues opened, new pull requests opened.

A single job uses `actions/add-to-project` to automatically add newly opened issues and PRs to the project board. The board URL is read from the `PROJECT_BOARD_URL` repository variable; a personal access token stored in `ADD_TO_PROJECT_PAT` authorises the action.

## Container Images

The three images built by `build-images.yaml` and their roles in the pack:

| Image | Purpose |
|-------|---------|
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/operator` | Kubernetes controller that reconciles `LLMModel` CRs |
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/model-downloader` | Init container that downloads model weights into a PVC before the serving pod starts |
| `ghcr.io/nebari-dev/nebari-llm-serving-pack/key-manager` | Web service and UI for managing per-user API keys |

A fourth image, `ghcr.io/llm-d/llm-d-cuda`, is the upstream llm-d GPU serving image. Its version is set in `values.yaml` under `defaults.serving.image` and is not built by this repository. See the [llm-d project](https://github.com/llm-d/llm-d) for its release history.

## Chart Versioning

The Helm chart lives at `charts/nebari-llm-serving/` and has two version fields in `Chart.yaml`:

- **`version`** - the chart packaging version (SemVer). Helm uses this for `helm repo` indexing and upgrade resolution.
- **`appVersion`** - the application version string, which the chart uses as the default image tag for the operator and key-manager when no explicit tag override is given.

Both fields are kept in sync; every release bumps them together to the same value (e.g. `0.1.0-alpha.9`). The current version is `0.1.0-alpha.9`.

## Release Process

Releases follow a manual workflow. As of `v0.1.0-alpha.9` there is no automated Helm repository publish step; the release consists of:

1. **Bump chart version** - edit `charts/nebari-llm-serving/Chart.yaml` and update both `version` and `appVersion` to the new value. Commit with a message like `chore(release): cut v0.1.0-alpha.X`.
2. **Push a `v*` tag** - push an annotated or lightweight tag matching the new version (e.g. `git tag v0.1.0-alpha.9 && git push origin v0.1.0-alpha.9`). This tag triggers `build-images.yaml`, which pushes images with the version tag to GHCR.
3. **Create a GitHub release** - the release is created manually (or as a draft) on the GitHub releases page. Release notes summarise the PRs included since the previous release.

There is currently **no automated Helm chart repository or OCI chart publish step**. Users install the chart directly from a local checkout or from the repository source. A future task will add automated chart publishing to a Helm OCI registry or GitHub Pages-hosted index.

To install from the current release, see the [Installation](/installation/) guide. To work with a local development build, see the [Local Development](/local-development/) page.

## Known Gaps

- There is no chart-testing (`ct lint`) step that validates the chart against multiple Kubernetes versions. Contributions adding `helm/chart-testing-action` are welcome.
- There is no automated Helm chart publish. The release process is entirely manual after the images are pushed.
- The `v0.1.0-alpha.9` GitHub release is currently a draft and has not been formally published.
