# Frontend Rewrite Plan — React + TypeScript (issue #92)

Rebuild the LLM Serving Pack UI from vanilla HTML/CSS/JS into a React + TypeScript
app (Vite + shadcn/ui + Tailwind v4), served as its own image, at feature parity
with the current API Key Manager.

Tracking issue: **#92 — Rebuild the LLM Serving Pack UI with React + TypeScript**

---

## Locked decisions

- **Design system:** copy the `@theme` tokens + `ThemeProvider`/`useThemePreference`
  dark-mode pattern from **nebari-landing**. No external design-system package.
- **Delivery:** serve the frontend **separately** as its own nginx image — not
  embedded in the Go `key-manager` binary.
- **Location:** the app lives at the repo root in **`frontend/`** (its own component,
  own Dockerfile, own CI job), consistent with `operator/`, `key-manager/`,
  `model-downloader/`.
- **Stack:** React 19 + TS + Vite 7, Tailwind v4 (`@tailwindcss/vite`),
  shadcn/ui + radix-ui, lucide-react, TanStack Query v5, Jotai v2, Biome 2,
  Vitest 4 + Testing Library. npm, dev port 5173.
- **Auth: Model B — SPA-managed Keycloak** (matches nebari-landing). The SPA
  owns login via `keycloak-js` (`onLoad: "login-required"` + PKCE) and attaches
  `Authorization: Bearer <token>` on every `/api` call (refresh on 401, retry
  once, else redirect to login). Keycloak `{ url, realm, clientId }` is loaded
  at runtime from **`/config.json`** (Helm-rendered, mounted into nginx — no
  rebuild to change). **Correction to the original plan:** auth is **not**
  enforced at the gateway. The `NebariApp` uses `enforceAtGateway: false` +
  `spaClient.enabled: true` (the operator provisions a public PKCE client), so
  there is no Envoy JWT `SecurityPolicy` and no oauth2-proxy sidecar. Instead the
  **key-manager validates the bearer in-process** against the Keycloak realm's
  JWKS (RSA signature + `exp` with 30s leeway + exact `iss` match; audience not
  checked), reading identity/groups from the claims — a real backend change,
  mirroring nebari-landing. It is configured by `LLM_KEYCLOAK_URL`,
  `LLM_KEYCLOAK_REALM`, and `LLM_KEYCLOAK_ISSUER_URL`.

## Reference template — nebari-landing (`~/repos/nebari-landing`)

Same architecture (Go backend + separate `frontend/` Vite app). Copy-ready patterns:

| Concern | File in nebari-landing |
|---|---|
| Build → nginx image, SPA `try_files` fallback | `frontend/Dockerfile` |
| `@` alias + `/api` dev proxy to `localhost:8080` | `frontend/vite.config.ts` |
| Semantic tokens, light/dark, `@custom-variant dark` | `frontend/src/app/index.css` |
| System-default theme (localStorage + `matchMedia`) | `frontend/src/hooks/useThemePreference.ts`, `ThemeContext.tsx` |

---

## Current state (what we're replacing)

The UI is a single-page **API Key Manager** embedded in the Go binary
(`//go:embed static/*` → `http.FileServer`):

- `key-manager/internal/ui/static/{index.html, app.js, style.css, *.svg}` (~950 LOC)
- `key-manager/internal/ui/embed.go`
- static serving wired in `key-manager/cmd/main.go:110-114`

**Backend API (unchanged by this work):**

- `GET /api/me`, `GET /api/models`, `GET /api/keys`, `POST /api/keys`,
  `DELETE /api/keys/{namespace}/{model}/{clientID}`, `/logout`
- OIDC-cookie auth middleware applied to `/api/*` only.

**Functional surface to preserve (one page):**

- Topbar: Nebari logo + account dropdown (name/email, Sign out → `/logout`)
- Page header + global error banner
- "My API Keys" card: Create button + table (Name/Description, Client ID, Model,
  Created, kebab → Revoke); loading / empty / error states
- Dialogs: Create Key (model select + description + validation), Key Created
  (client ID, one-time key, copy, download `.txt`, warning), Revoke confirmation

---

## Auth + deployment shape (Model B)

Auth is **SPA-managed Keycloak with bearer tokens** (see Locked decisions). This
relaxes the same-origin cookie constraint — the token rides in the
`Authorization` header, not a host-scoped cookie — but we still serve the SPA and
API under one host for simplicity and so the JWT `SecurityPolicy` covers both.

**Deployment approach:** the **nginx frontend container is the service the
NebariApp targets**; nginx serves the SPA + `/config.json` and reverse-proxies
`/api/*` to the key-manager ClusterIP (`:8080`). The Go key-manager drops static
serving and becomes API-only. **As built (corrected):** the gateway does **not**
enforce auth (`enforceAtGateway: false`, public `spaClient`) — there is no Envoy
JWT `SecurityPolicy`. The **key-manager validates the bearer itself** against
Keycloak's JWKS, so it now verifies the signature (previously it only parsed
claims). The key-manager Service stays internal-only and is never exposed
through the gateway.

Login/logout are driven by `keycloak-js` in the SPA (redirect to Keycloak), so
the old `/logout` proxy route is replaced by `keycloak.logout()`.

---

## Phases

### Phase 1 — Scaffold `frontend/` ✅
- [x] Vite + React 19 + TS project at repo-root `frontend/`
- [x] Tailwind v4 via `@tailwindcss/vite`; shadcn (`components.json`) + radix-ui
- [x] Deps: lucide-react, TanStack Query v5, Jotai v2, Biome 2, Vitest 4 + Testing Library
- [x] `package.json` scripts: `dev`, `build` (`tsc -b && vite build`), `preview`,
      `check` (biome), `test` / `test:run` (vitest)
- [x] `vite.config.ts` with `@` alias + `/api` (and `/logout`) dev proxy → `http://localhost:8080`
- [x] Quality gate green: `npm run build`, `npm run test:run`, `npm run check` all pass

### Phase 2 — Theming ✅
- [x] Copy nebari-landing tokens into `src/index.css` (light/dark, `@theme inline`, `@custom-variant dark`)
- [x] Copy `useThemePreference.ts` + `useLocalStorageState.ts`; add `ThemeProvider` (`src/providers/ThemeProvider/`); system default
- [x] `ThemeProvider` wired into `main.tsx`
- [x] `components.json` baseColor/aliases aligned (style `radix-vega`, css `src/index.css`)
- [x] Test setup mocks (`localStorage`, `matchMedia`) so theme hooks test cleanly; gate green
  - Note: `npm test` runs vitest once (`vitest --run`); no separate `test:run`.

### Phase 3 — Data + state layer ✅
- [x] `lib/api.ts` fetch wrapper (`api.get/post/delete`, `ApiError` w/ status, JSON, 204 → null)
- [x] `lib/types.ts` — API shapes; `RawModelInfo` (PascalCase, no Go json tags) normalized to `Model`
- [x] `lib/queryClient.ts` — configured `QueryClient` (retry off, 30s staleTime)
- [x] TanStack Query hooks: `useCurrentUser`, `useModels`, `useApiKeys` + `useCreateKey` + `useRevokeKey`
      (mutations invalidate `["keys"]`)
- [x] `store/dialogAtoms.ts` — Jotai discriminated-union dialog atom (none/create/created/revoke)
- [x] `QueryClientProvider` + `ThemeProvider` wired in `main.tsx`
- [x] Tests: `api.test.ts`, `useApiKeys.test.tsx`; gate green (11 tests)

### Phase 4 — Components (feature parity) ✅
Each component gets its own PascalCase dir + `index.ts` barrel.
- [x] `Topbar` (logo, account dropdown: name/email, theme radio light/dark/system, Sign out → `/logout`)
- [x] `KeysCard` (table + loading/empty/error states + Create button)
- [x] `KeyRowActions` (kebab → Revoke, destructive)
- [x] `CreateKeyDialog` (model select + description + validation)
- [x] `KeyCreatedDialog` (client ID, one-time key, copy w/ feedback, download `.txt`, warning)
- [x] `RevokeKeyDialog` (destructive confirm)
- [x] `ErrorBanner` (dismissible, driven by `errorAtom`)
- [x] shadcn primitives in `components/ui/`: button, card, table, dropdown-menu, avatar,
      input, badge (copied from nebari-landing) + dialog, select, label, alert (shadcn CLI)
- [x] Dialogs driven by `dialogAtom`; `lib/format.ts` (date + initials); `App.tsx` composes the page
- [x] Tests: `App.test.tsx`, `KeysCard.test.tsx`, `src/test/render.tsx` provider helper; gate green (14 tests)

> Not yet done: visual verification against a **live backend** + Figma comparison. Deferred to
> Phase 6 (local dev brings up the API) / Phase 7 (polish). The build, type-check, and tests pass.

### Phase 4b — Auth (Model B: SPA-managed Keycloak) ✅
Mirrors nebari-landing's `src/auth/*`, `src/api/client.ts`, `src/app/config.ts`.
- [x] Add `keycloak-js` dependency
- [x] `src/app/config.ts` — load + cache `/config.json` (`keycloak: { url, realm, clientId }`, optional title)
- [x] `src/auth/keycloak.ts` — `initKeycloak()` (`login-required` + PKCE), `getToken()` w/ refresh,
      `SessionExpiredError`, `signOut()` → `keycloak.logout()`, `__PW_E2E_AUTH__` shim
- [x] `src/auth/user.ts` — `useUser()` reads identity from the ID token; `Topbar` now uses it
- [x] `api.ts` — attach `Authorization: Bearer <token>`; on 401 refresh + retry once
- [x] `main.tsx` — top-level `await loadAppConfig()` + `await initKeycloak()` before render
- [x] `Topbar` Sign out → `signOut()` (dropped the `/logout` anchor)
- [x] Test setup injects the `__PW_E2E_AUTH__` shim so api calls get a token; `public/config.json` gitignored
- [x] Tests updated/added for the bearer + 401-retry path; gate green (15 tests)

> Note: `src/hooks/useCurrentUser.ts` (GET /api/me) is no longer used by the UI (identity now
> comes from the ID token) but kept as a valid endpoint wrapper. Remove in cleanup if still unused.

### Phase 5 — Serve separately (Docker + Helm + CI) ✅
- [x] `frontend/Dockerfile` (node build → nginx; SPA `try_files`; `location /api`
      `proxy_pass` to key-manager service)
- [x] `frontend/nginx.conf` — also serves `/config.json` (Helm-rendered, mounted)
- [x] Gut `key-manager/internal/ui/` (remove `embed.go` + `static/`); drop file
      server / SPA route from `cmd/main.go` → key-manager is API-only
- [x] Helm: new `frontend` Deployment + Service + config ConfigMap
- [x] Helm: render `/config.json` (ConfigMap from `frontend.keycloak.*` values) into nginx
- [x] Repoint `key-manager-nebariapp.yaml` `service:` at the frontend service
- [x] Gateway auth is **Model B without a gateway `SecurityPolicy`**: NebariApp
      `enforceAtGateway: false` + `spaClient.enabled: true`; the **key-manager**
      validates the bearer against Keycloak JWKS (`LLM_KEYCLOAK_*` env). (Not the
      original Envoy JWT `SecurityPolicy` plan.)
- [x] Add `frontend.image.*` + `frontend.keycloak.*` (and `keyManager.keycloak.*`) to `values.yaml`
- [x] CI: `build-frontend` job in `build-images.yaml`
- [x] CI: `lint-frontend` (biome) + `test-frontend` (vitest) jobs in `lint.yaml` / `test.yaml`

### Phase 6 — Local dev
- [x] Dev Keycloak in the kind cluster: `dev/manifests/keycloak.yaml` (`start-dev
      --import-realm`) + `dev/keycloak/realm-nebari.json` (realm `nebari`, public
      PKCE client `nebari-frontend-spa`, groups mapper, `testuser`/`testuser`);
      `make deploy-keycloak` renders the realm ConfigMap + deploys; `make
      pf-keycloak` (8180) / `pf-key-manager` (8080) port-forward helpers
- [x] Wire `frontend/` into `dev/`: `cd dev && ./run-dev.sh` (also `make run-dev`
      / `make ui`) brings up the kind cluster + dev-mode key-manager
      (`LLM_DEV_MODE=true`) + Vite dev server at :5173 with `VITE_DEV_NO_AUTH=true`,
      proxying `/api/*` to the port-forwarded key-manager on :8080. The old
      `go:embed` static UI and the `dev/uidev` Go live-reload server are removed.
- [x] Document Vite `/api` proxy + local `config.json` + how Keycloak/bearer auth is
      stubbed for standalone runs (E2E auth shim or a real Keycloak)

> Inner loop: `cd dev && make setup build-images load-images deploy deploy-keycloak`,
> then `make pf-keycloak` + `make pf-key-manager` (separate terminals), then
> `cd frontend && npm run dev`. `frontend/public/config.json` already points at
> `http://localhost:8180` / realm `nebari` / client `nebari-frontend-spa`. Mint a
> token without the browser via the client's direct-access grant:
> `curl -d client_id=nebari-frontend-spa -d username=testuser -d password=testuser
> -d grant_type=password http://localhost:8180/realms/nebari/protocol/openid-connect/token`

### Phase 7 — Quality gate, cleanup, docs
- [x] `npm run build && npm run test:run && npm run check` all pass
- [x] Remove old vanilla files (`key-manager/internal/ui/`, `dev/uidev`) after parity confirmed
- [x] Refresh README / getting-started UI references (screenshots unchanged)

---

## Open items — resolved during build

- [x] `NebariApp` routes a **single** service under the host (the frontend nginx),
      which reverse-proxies `/api/*` to the key-manager — so the nginx `/api` proxy
      stays. (No two-services-per-host routing needed.)
- [x] The nginx proxy targets the internal key-manager ClusterIP Service on `:8080`
      over in-cluster DNS; that Service is never exposed through the gateway.
- [x] Gateway does **not** use a JWT `SecurityPolicy` for the key-manager host.
      Auth is `enforceAtGateway: false` + a public `spaClient`; the SPA reads
      `keycloak.{url,realm,clientId}` from `/config.json` (from `frontend.keycloak.*`).
- [x] **Decided: the key-manager validates the bearer signature itself** against
      Keycloak JWKS (`LLM_KEYCLOAK_*`), rather than trusting the gateway. This was a
      real backend change (the middleware now verifies signatures).

---

## Acceptance criteria (from #92)

- [ ] Vite + React + TS scaffold per `frontend-dev` conventions
- [ ] Tailwind v4 wired to Nebari design-system tokens (semantic, not hardcoded)
- [ ] shadcn/ui via `components.json`; UI composed on design-system components
- [ ] All existing screens reimplemented at feature parity
- [ ] State/data fetching per conventions (TanStack Query + Jotai where applicable)
- [ ] UI matches Figma + uses shared design-system components
- [ ] Dark-mode via profile menu, defaulting to System
- [ ] Biome + Vitest gates pass (`biome check`, `vitest run`)
- [ ] Build/dev/lint/test scripts in `package.json`
- [ ] Old vanilla frontend removed once parity confirmed
