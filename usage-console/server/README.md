# Stargate control plane (API-first slice)

A Go control plane serving the console over REST + SSE. It uses two
databases: Postgres for config, keys and audit, and Postgres + TimescaleDB
for receipts (spec §4.6). There is also a dev request path, so the console
shows real traffic before Envoy AI Gateway and Warden are in place.

```
trafficgen ──► devgateway ──► fake-openai          (OpenAI-compatible, per-backend behaviour)
                  │  identity · budget · rules · routing/fallback · response inspection
                  ▼
            receipts db (Timescale) ── NOTIFY ──► stargate-api ──► console
            config db (Postgres) ◄───────────────┘   REST /api/v1/{tenant}/…  SSE /stream/traffic
```

## Run it

```sh
cd server
make migrate                    # start both databases, migrate, seed the demo tenant
make backfill                   # optional: 7 days of synthetic history so charts have shape
make dev                        # fake upstream :8090, API :8080, gateway :8081, traffic ~0.8 rps
cd ../console && npm run dev:api   # console on the API (npm run dev stays on mock data)
```

Everything uses the demo tenant. Seeded keys authenticate with
`<prefix>_devsecret_not_for_production`, for example:

```sh
curl localhost:8081/v1/chat/completions \
  -H 'Authorization: Bearer ngw_live_7f3a_devsecret_not_for_production' \
  -d '{"model":"gpt-5-mini","messages":[{"role":"user","content":"hello"}]}'
```

The `X-Stargate-Receipt` response header names the receipt. `X-Data-Region: eu`
triggers the `eu-only` reroute.

## Commands

| | |
|---|---|
| `cmd/stargate-api serve` | REST + SSE on :8080. Migrates and seeds on start. Also `migrate`, and `backfill -days N -per-day N`. |
| `cmd/devgateway` | `POST /v1/chat/completions` on :8081. Reloads config from the db every 5s. |
| `cmd/fake-openai` | `POST /{backend}/v1/chat/completions` on :8090, with streaming. |
| `cmd/trafficgen` | Poisson traffic at `-rps`, with the mockup's mix of keys, PII, secrets and EU requests. |

DB URLs come from `STARGATE_CONFIG_DB` and `STARGATE_RECEIPTS_DB`. The
defaults match `compose.yaml`.

## API

All paths are under `/api/v1/{tenant}`. JSON field names match
`console/src/data/mock.ts`.

- `GET teams | models | backends | routes | keys | budgets | rules | detectors | changes`
- `POST keys` returns `{key, secret}`; the secret is shown once and only its sha256 is stored.
- `POST keys/{id}/revoke`
- `POST keys/{id}/rotate` with `{overlapHours}`. Both secrets work until the overlap ends.
- `GET receipts?limit&before`, `GET receipts/{id}`
- `GET series/traffic?range=15m|1h|6h|24h|7d|30d` gives verdict counts per bucket, from `receipts_5m`.
- `GET series/spend?days=30` gives daily spend by team, from `receipts_daily`.
- `GET stream/traffic?key&team&model&verdict&backend` is SSE. Each insert or settle sends a `receipt` event. Streamed requests arrive twice: first in flight, then settled.

Every mutation writes an `audit_log` row in the same transaction.

## Not yet (by design for this slice)

- **Auth.** There's no OIDC yet; every caller is `dev@localhost`. There's also no ETag/If-Match and no `dryRun`.
- **Mutations.** Only keys are writable. Backends, routes, rules and budgets are read-only over the API; the console still edits those in local state.
- **Rule engine.** It understands the demo rules' condition and action forms, with regex detectors. Warden replaces it.
- **Routing.** Routes feed the fallback lists only. Key `allowedRegions` isn't enforced, and backend health is configured rather than probed.
- **Queries.** Rule fire counts and backend p50 read raw receipts. Continuous aggregates can't unnest jsonb or compute percentiles incrementally.
- **Response inspection.** It cuts a stream at an exfil URL, but a pattern split across chunks can leak its first part.
