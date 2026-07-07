---
title: Key-manager UI development
---
A one-command local environment for working on the key-manager web UI, with a
populated model list and hot reload. You do not need to understand the operator,
the gateway, or Kubernetes to use it.

## What you get

The UI is a React 19 + TypeScript app (Vite, Tailwind, shadcn/ui) that lives in
`frontend/` at the repo root. It is a standalone single-page app: in production
it is built to a static bundle and served by its own nginx container image, and
it talks to the key-manager only over `/api/*`.

`make run-dev` (or `cd dev && ./run-dev.sh`) brings up a local
[kind](https://kind.sigs.k8s.io/) cluster with the operator, the key-manager (in
dev mode, so no Keycloak login), and three OpenRouter passthrough models, then:

- port-forwards the key-manager API to `http://localhost:8080`, and
- starts the Vite dev server at `http://localhost:5173` with hot module reload,
  proxying `/api/*` to the port-forwarded key-manager.

You develop against `http://localhost:5173`.

## Prerequisites

- Docker, [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation), kubectl, helm, and Go 1.25+.
- [Node.js](https://nodejs.org) 20+ and npm (for the Vite dev server).
- An [OpenRouter API key](https://openrouter.ai/keys).

## Setup

```bash
cd dev
cp .env.example .env
# edit .env and set OPENROUTER_API_KEY=sk-or-v1-...
./run-dev.sh          # or: make run-dev  (from dev/)  ·  make ui  (from repo root)
```

First run creates the cluster and builds images, so it takes a few minutes.
Later runs reuse the cluster and start in seconds. When it is ready you will see:

```
key-manager API:   http://localhost:8080
Vite dev server:   http://localhost:5173   <-- develop here
```

Open `http://localhost:5173`. The dev server runs with `VITE_DEV_NO_AUTH=true`,
so the Keycloak PKCE login is bypassed and you are signed in automatically as
user `dev` (group `llm`). The model list shows `claude-sonnet-45`,
`gemini-25-flash`, and `llama-33-70b`. Mint and revoke keys against them as a
real user would.

Press Ctrl-C to stop; the port-forward is cleaned up for you. The cluster keeps
running for the next `make run-dev`.

## Editing the UI

The React source lives under `frontend/src/`. Edit any file there and the Vite
dev server at `:5173` hot-reloads the browser. The dev server proxies `/api/*`
to the port-forwarded key-manager, so the UI talks to a real backend (real
models, real key minting) while you iterate on components, styles, and state.

### Auth in dev mode

In production the SPA owns login: `keycloak-js` performs a `login-required` PKCE
redirect to Keycloak, obtains an access token, and the app sends
`Authorization: Bearer <token>` on every `/api` call. The dev loop shortcuts
this on both ends:

- The Vite dev server sets `VITE_DEV_NO_AUTH=true`, which skips the keycloak-js
  redirect so you never hit a login screen.
- The in-cluster key-manager runs with `LLM_DEV_MODE=true`, which bypasses auth
  and injects a fixed identity (`dev`, group `llm`). So `/api/me` returns that
  identity and every `/api/*` call works without a token.

Never enable dev mode in a real deployment.

## Frontend quality gate

Before opening a PR, run the same checks CI runs, from `frontend/`:

```bash
cd frontend
npm run build   # tsc -b && vite build
npm test        # vitest
npm run check   # biome lint + format
```

## Shipping a change

The UI ships as its own image
(`ghcr.io/nebari-dev/nebari-llm-serving-pack/frontend`, nginx serving the built
bundle) - it is no longer embedded in the Go key-manager binary. Committing your
edits to `frontend/` is all that is needed for them to ship in the next image
build. To see your changes in the actual in-cluster pod (rather than the dev
server), rebuild and reload:

```bash
make build-images && make load-images
kubectl -n llm-operator-system rollout restart deployment/llm-frontend
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

- **`SSL_ERROR_RX_RECORD_TOO_LONG` / "Secure Connection Failed"** - the dev
  server is plain HTTP. Use `http://localhost:5173` (or `:8080`), not `https://`.
  If your browser force-upgrades localhost to HTTPS, use a private window or
  disable HTTPS-Only mode for the site.
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
