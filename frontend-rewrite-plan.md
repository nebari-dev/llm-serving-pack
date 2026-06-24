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

## Auth constraint that shapes deployment

The Keycloak cookie set by the NebariApp gateway is scoped to **one hostname**, so
the SPA (`/`) and the API (`/api`, `/logout`) must share a host. The `NebariApp` CRD
targets a single `service:port`.

**Primary approach:** the **nginx frontend container is the service the NebariApp
targets**, and nginx reverse-proxies `/api/*` and `/logout` to the key-manager
ClusterIP (`:8080`). The Go key-manager drops static serving and becomes API-only.

**Fallback** (only if confirmed supported): gateway-level path routing to two
services under one host — would let us skip the nginx `/api` proxy.

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

### Phase 4 — Components (feature parity)
Each component gets its own PascalCase dir + co-located test + `index.ts` barrel.
- [ ] `Topbar` (logo, account dropdown: name/email, theme submenu light/dark/system, Sign out → `/logout`)
- [ ] `KeysCard` (table + loading/empty/error states + Create button)
- [ ] `KeyRowActions` (kebab → Revoke)
- [ ] `CreateKeyDialog` (model select + description + validation)
- [ ] `KeyCreatedDialog` (client ID, one-time key, copy, download `.txt`, warning)
- [ ] `RevokeKeyDialog` (destructive confirm)
- [ ] `ErrorBanner` / toast
- [ ] shadcn primitives added: button, dialog, table, select, input, label,
      dropdown-menu, alert, card, avatar, sonner

### Phase 5 — Serve separately (Docker + Helm + CI)
- [ ] `frontend/Dockerfile` (node build → nginx; SPA `try_files`; `location /api` +
      `/logout` `proxy_pass` to key-manager service)
- [ ] `frontend/nginx.conf`
- [ ] Gut `key-manager/internal/ui/` (remove `embed.go` + `static/`); drop file
      server / SPA route from `cmd/main.go` → key-manager becomes API-only
- [ ] Helm: new `frontend` Deployment + Service (nginx :80, backend URL via env)
- [ ] Repoint `key-manager-nebariapp.yaml` `service:` at the frontend service
- [ ] Add `frontend.image.*` to `values.yaml`
- [ ] CI: `build-frontend` job in `build-images.yaml`
- [ ] CI: `lint-frontend` + test job (biome + vitest) in `lint.yaml` / `test.yaml`

### Phase 6 — Local dev
- [ ] Wire `frontend/` into `dev/` (Makefile / manifests) alongside the mock backend
- [ ] Document Vite `/api` proxy + how auth is faked/bypassed locally (cookie middleware
      needs a dev shim or a running gateway)

### Phase 7 — Quality gate, cleanup, docs
- [ ] `npm run build && npm run test:run && npm run check` all pass
- [ ] Remove old vanilla files only after parity confirmed
- [ ] Refresh `docs/install-production-screenshots/*` + README / getting-started UI references

---

## Open items to confirm during build

- [ ] Can `NebariApp` route two services under one host? (Would enable the fallback
      and let us drop the nginx `/api` proxy.) Default assumes **no**.
- [ ] Confirm the key-manager ClusterIP service name/port the nginx proxy targets.

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
