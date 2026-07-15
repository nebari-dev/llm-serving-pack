---
title: CI/CD and Releasing
---
This page documents every GitHub Actions workflow in the repository, the container images they produce, and the release process used to cut new versions. It is aimed at contributors and maintainers.

## Workflows

This section covers the workflows in `.github/workflows/` relevant to CI and releasing. The image build and chart release workflows are thin callers of reusable workflows shared across Nebari packs, defined in [`nebari-dev/.github`](https://github.com/nebari-dev/.github): `pack-build-image.yaml` and `pack-release.yaml`. This repository's own `build-images.yaml` and `release.yaml` files just supply the pack-specific inputs (image names, build contexts, the chart path, which `values.yaml` keys to pin); the actual build, pin, package, and publish steps live in the shared workflows, so every pack that adopts them gets the same behavior and bug fixes.

### Test (`test.yaml`)

**Triggers:** push to `main`, pull requests targeting `main`.

Runs three parallel jobs:

| Job | Component | Command |
|-----|-----------|---------|
| `test-operator` | `operator/` | `make test` |
| `test-key-manager` | `key-manager/` | `go test ./...` |
| `test-frontend` | `frontend/` | `npm test` (vitest) |

The two Go jobs set up Go from the component's own `go.mod`/`go.sum`, so each component pins its Go version independently. The `test-frontend` job runs on Node with the `frontend/` npm lockfile.

### Lint (`lint.yaml`)

**Triggers:** push to `main`, pull requests targeting `main`.

Runs four parallel jobs:

| Job | Component | Tool |
|-----|-----------|------|
| `lint-operator` | `operator/` | `golangci-lint` v2.4.0 |
| `lint-key-manager` | `key-manager/` | `golangci-lint` v2.4.0 |
| `lint-frontend` | `frontend/` | `biome` (`npm run check`) |
| `lint-helm` | `charts/nebari-llm-serving/` | `helm lint` |

The Helm lint job runs `helm lint charts/nebari-llm-serving/` against the chart without any value overrides, so `platform.baseDomain` defaults to empty and any chart template that gates on it must handle the empty case.

### Build Images (`build-images.yaml`)

**Triggers:** push to `main` touching `operator/`, `key-manager/`, `frontend/`, `model-downloader/`, the workflow file itself, or `charts/nebari-llm-serving/Chart.yaml`; pull requests touching those same component paths (builds the image but does not push it; chart-only PRs do not trigger a build); manual `workflow_dispatch`.

The `Chart.yaml` path is in the push trigger for a specific reason: a release is cut by bumping `version` in that file (see [Releasing](#releasing)), and the release workflow pins the release commit's images to `sha-<that commit>`. Triggering a build on the same push that cuts the release ensures those shas exist.

Each of the four images (operator, model-downloader, key-manager, frontend - React SPA served by nginx) has its own job in `build-images.yaml`, and each job is a one-line call into the shared `pack-build-image.yaml` workflow, passing the image name, build context, and Dockerfile path. The shared workflow does the actual work: it builds with Docker Buildx and, on a push, pushes the image to two registries:

| Image | Build context | Dockerfile |
|-------|--------------|------------|
| `operator` | `operator/` | `operator/Dockerfile` |
| `model-downloader` | `model-downloader/` | `model-downloader/Dockerfile` |
| `key-manager` | `.` (repo root) | `key-manager/Dockerfile` |
| `frontend` | `frontend/` | `frontend/Dockerfile` |

- GitHub Container Registry, as `ghcr.io/nebari-dev/llm-serving-pack/<image>`
- Quay, as `quay.io/nebari/llm-serving-pack-<image>`

`docker/metadata-action` derives the tags automatically:

| Condition | Tag applied |
|-----------|------------|
| Any push (or manual dispatch) | `sha-<short SHA>` |
| Push to the default branch (`main`) | also `latest` |

Pull request builds always run with pushing disabled, so a PR proves the image builds without publishing anything under that PR's commit.

Authentication uses the workflow's automatic `GITHUB_TOKEN` for GHCR (`packages: write`) and a `QUAY_TOKEN` secret plus a `QUAY_USERNAME` repository variable for Quay.

Nothing here produces a version-numbered tag. On `main`, the chart's `values.yaml` hardcodes `tag: "latest"` for the operator, key-manager, frontend, and model-downloader images - that is deliberate, and covered in [Chart Versioning](#chart-versioning) and [Releasing](#releasing) below.

### Release Chart (`release.yaml`)

**Triggers:** push to `main` that touches `charts/nebari-llm-serving/Chart.yaml`.

This is the workflow that actually cuts a release. It is a thin caller of the shared `pack-release.yaml` workflow: `release.yaml` just tells it which chart to package (`charts/nebari-llm-serving`), what to call it (`nebari-llm-serving`), and which `values.yaml` keys hold image tags that need to be pinned (`operator.image.tag`, `keyManager.image.tag`, `frontend.image.tag`, `modelDownloader.image.tag`). The full mechanics of what the shared workflow does are described in [Releasing](#releasing) below.

### Docs (`docs.yml`)

**Triggers:** push to `main` and pull requests touching `docs/**`, `scripts/check-links.sh`, or the workflow file itself; manual `workflow_dispatch`.

A single **`docs`** job on Node 22 (npm cache keyed on `docs/package-lock.json`): runs `npm ci`, installs Playwright Chromium (needed to render Mermaid diagrams to SVG at build time), runs `npm test`, then computes the correct `site` and `base` values (production: `https://packs.nebari.dev` + `/llm-serving-pack/`; preview: `https://<alias>.llm-serving-pack.pages.dev` + `/`). It then runs `npm run build` and validates internal links by running `scripts/check-links.sh` (with `SKIP_BUILD=1`) against the built `docs/dist`.

For non-fork PRs and pushes to `main`, the job deploys to Cloudflare Pages via `cloudflare/wrangler-action`. On pull requests from the same repo, a sticky comment is posted with the preview URL (`pages-deployment-alias-url`). Fork PRs run the build and link-check but skip deploy (secrets are unavailable).

### Docs preview cleanup (`docs-preview-cleanup.yml`)

**Triggers:** a pull request closing (merged or not).

When a PR closes, this workflow deletes the Cloudflare Pages **preview** deployments for that PR's branch (via the Pages API, matched by branch and filtered to the preview environment). Direct Upload deploys are not tied to the git branch lifecycle, so without this, preview deployments would linger after the branch is gone. Fork PRs are skipped (no preview, no secrets).

### Add to Project (`add-to-project.yaml`)

**Triggers:** new issues opened, new pull requests opened.

A single job uses `actions/add-to-project` to automatically add newly opened issues and PRs to the project board. The board URL is read from the `PROJECT_BOARD_URL` repository variable; a personal access token stored in `ADD_TO_PROJECT_PAT` authorises the action.

## Container Images

The four images built by `build-images.yaml` and their roles in the pack:

| Image | Purpose |
|-------|---------|
| `ghcr.io/nebari-dev/llm-serving-pack/operator` | Kubernetes controller that reconciles `LLMModel` CRs |
| `ghcr.io/nebari-dev/llm-serving-pack/model-downloader` | Init container that downloads model weights into a PVC before the serving pod starts |
| `ghcr.io/nebari-dev/llm-serving-pack/key-manager` | Key manager REST API for managing per-user API keys |
| `ghcr.io/nebari-dev/llm-serving-pack/frontend` | LLM serving pack React UI (nginx) - serves the SPA and proxies `/api` to the key-manager |

A fifth image, `ghcr.io/llm-d/llm-d-cuda`, is the upstream llm-d GPU serving image. Its version is set in `values.yaml` under `defaults.serving.image` and is not built by this repository. See the [llm-d project](https://github.com/llm-d/llm-d) for its release history.

## Chart Versioning

The Helm chart lives at `charts/nebari-llm-serving/` and has two version fields in `Chart.yaml`:

- **`version`** - the chart packaging version (SemVer). Bumping this field and merging the change to `main` is what cuts a release; see [Releasing](#releasing) below.
- **`appVersion`** - the application version string. The chart only uses it to set the `app.kubernetes.io/version` label on the resources it renders. It has no effect on which image gets deployed.

Both fields are conventionally bumped together (e.g. to `0.1.0-alpha.9`), but only `version` drives the release automation. The current version is `0.1.0-alpha.9`.

Image tags are not derived from `appVersion`, and they are not hand-edited in the repository either. On `main`, every image tag in `values.yaml` (operator, key manager, frontend, model downloader) is pinned to `latest` on purpose: the real, immutable tag only gets written when a release is packaged, and that step never touches the repo tree. See [Releasing](#releasing) for where that pinning happens.

## Releasing

Cutting a release is one action: open a pull request that bumps `version` in `charts/nebari-llm-serving/Chart.yaml`, and merge it. Everything else is automatic - no git tag to push by hand, no editing image tags, no release step that happens outside a PR merged to `main`.

**What happens when that PR merges:**

The merge commit (the `Chart.yaml` bump) touches a path that both `build-images.yaml` and `release.yaml` watch, so it triggers both workflows in parallel:

1. `build-images.yaml` builds images for the merge commit and pushes them to `ghcr.io` and `quay.io`, tagged `sha-<short>` (see [Build Images](#build-images-build-imagesyaml) above). This is what guarantees the sha the release pins in the next step actually has matching images.
2. `release.yaml` runs. It reads the new `version` from `Chart.yaml`, pins that exact sha into the four image tags in the chart's `values.yaml` (operator, key manager, frontend, model downloader), packages the chart with `helm package`, and creates a GitHub Release tagged `nebari-llm-serving-<version>` with auto-generated notes and the packaged chart attached. It then opens a pull request that syncs the pinned chart source to `nebari-dev/helm-repository`; once that PR merges, the chart is published to `oci://quay.io/nebari/charts/nebari-llm-serving`.
3. A pre-release version (anything with a `-` in it, e.g. `0.2.0-alpha.1`) is marked as a pre-release on the GitHub Release automatically.

There is no hard gate between the two workflows in v1: `release.yaml` does not wait for `build-images.yaml` to finish before pinning shas. In practice both jobs kick off from the same push and finish close together. If the image build for that commit fails or lags, the release still pins the sha, and that failure is visible immediately: the `build-images.yaml` run shows red, and any deploy of that version fails to pull the missing image. The fix is to re-cut the release (bump `version` again) once the underlying build issue is resolved, rather than to retroactively patch a published chart.

**Why `values.yaml` on `main` shows `tag: "latest"`:** the tag floats on purpose. Pinning happens in step 2 above, when a version is packaged, not in the repository tree. The published chart, not the source, is the pinned record of what a given version actually runs. To see exactly which image shas a released version deploys:

```
helm show values oci://quay.io/nebari/charts/nebari-llm-serving --version <version>
```

**Reproducibility:** installing a given `--version` always deploys the same shas, because those shas are baked into the packaged chart rather than read live from `main`. Re-running the release pipeline for a version that has already been released is a no-op: it checks whether a GitHub Release for that chart and version already exists, and skips everything if so.

To install a released version, see the [Installation](/installation/) guide. To work with a local development build, which reads `values.yaml` as committed (so `latest`-tagged images), see the [Local Development](/local-development/) page.

## Known Gaps

- There is no chart-testing (`ct lint`) step that validates the chart against multiple Kubernetes versions. Contributions adding `helm/chart-testing-action` are welcome.
