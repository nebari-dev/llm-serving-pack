---
title: Key-manager UI development
---
A one-command local environment for working on the key-manager web UI, with a
populated model list and hot reload. You do not need to understand the operator,
the gateway, or Kubernetes to use it.

## What you get

`make run-dev` brings up a local [kind](https://kind.sigs.k8s.io/) cluster with
the operator, the key-manager (in dev mode, so no Keycloak login), and three
OpenRouter passthrough models, then:

- port-forwards the key-manager to `http://localhost:8080` (its embedded UI + API), and
- starts a hot-reload dev server at `http://localhost:5173` that serves the UI
  from your working copy and reloads the browser when you edit it.

You develop against `http://localhost:5173`.

## Prerequisites

- Docker, [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation), kubectl, helm, and Go 1.25+.
- An [OpenRouter API key](https://openrouter.ai/keys).

## Setup

```bash
cd dev
cp .env.example .env
# edit .env and set OPENROUTER_API_KEY=sk-or-v1-...
make run-dev
```

First run creates the cluster and builds images, so it takes a few minutes.
Later runs reuse the cluster and start in seconds. When it is ready you will see:

```
key-manager (embedded UI + API): http://localhost:8080
hot-reload UI dev server:        http://localhost:5173   <-- develop here
```

Open `http://localhost:5173`. You are signed in automatically as user `dev`
(group `llm`), and the model list shows `claude-sonnet-45`, `gemini-25-flash`,
and `llama-33-70b`. Mint and revoke keys against them as a real user would.

Press Ctrl-C to stop; the port-forward is cleaned up for you. The cluster keeps
running for the next `make run-dev`.

## Editing the UI

The UI is plain static files (no build step):

```
key-manager/internal/ui/static/
  index.html
  app.js
  style.css
```

Edit any of them and the browser at `:5173` reloads automatically. The dev
server proxies `/api/*` to the port-forwarded key-manager, so the UI talks to a
real backend (real models, real key minting) while you iterate on markup, styles,
and JavaScript.

### Auth in dev mode

The key-manager normally sits behind Keycloak and the gateway's OIDC layer. On
this cluster it runs with `LLM_DEV_MODE=true`, which bypasses auth and injects a
fixed identity (`dev`, group `llm`). So `/api/me` returns that identity and every
`/api/*` call works without a token. Never enable dev mode in a real deployment.

## Shipping a change

The static files are compiled into the key-manager binary with `go:embed`, so
committing your edits is all that is needed for them to ship in the next image
build. To see your changes in the actual in-cluster pod (rather than the dev
server), rebuild and reload:

```bash
make build-images && make load-images
kubectl -n llm-operator-system rollout restart deployment/llm-key-manager
```

## API reference (what the UI calls)

All under `/api`, all gated by auth (bypassed in dev mode):

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/me` | Current user identity (username, groups). |
| GET | `/api/models` | Models the user may mint keys for. |
| GET | `/api/keys` | The user's existing keys. |
| POST | `/api/keys` | Mint a key. Body: `{"modelName": "<model>"}`. |
| DELETE | `/api/keys/{namespace}/{model}/{clientID}` | Revoke a key. |

## Troubleshooting

- **`SSL_ERROR_RX_RECORD_TOO_LONG` / "Secure Connection Failed"** - the UI is
  plain HTTP. Use `http://localhost:5173` (or `:8080`), not `https://`. If your
  browser force-upgrades localhost to HTTPS, use a private window or disable
  HTTPS-Only mode for the site.
- **Model list is empty** - the key-manager resyncs models every 30s; give it a
  moment after `make run-dev`, or check `kubectl -n llm-operator-system get passthroughmodel`.
- **`make run-dev` says `OPENROUTER_API_KEY is not set`** - create `dev/.env`
  from `dev/.env.example` and set the key.
- **Start over** - `make teardown` deletes the cluster; `make run-dev` rebuilds it.

## Related

- [Local Development](/local-development/) - the full local dev path (operator,
  passthrough models, inference).
- [Installation](/installation/) - real-cluster deployment with
  Keycloak and the gateway.
