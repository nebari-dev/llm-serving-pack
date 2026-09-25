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

There's also a second request path through the real gateway, Agent Router
(formerly Envoy AI Gateway), run standalone with `aigw run`, no Kubernetes:

```
trafficgen ──► Agent Router :1975 ──────────────────────► fake-openai
                  │  ▲  ext_authz: key + model check      (caller's key removed)
                  │  ├─► stargate-api :8082
                  │  │  ext_proc: budgets · rules · redact · reroute
                  │  └─► warden :8083
                  │  routing · retries · failover (aigw/config.yaml)
                  ▼  access log over OTLP/gRPC (+ Warden's decision)
            receipt-ingest :4317 ──► receipts db ── NOTIFY ──► stargate-api ──► console
```

A SecurityPolicy sends every request to stargate-api's ext_authz service
first. It checks the bearer key the way the dev gateway does, so rotation,
revocation and expiry behave the same, and checks the model against the key's
allowlist after aliasing. A bad key gets a 401 and a model the key can't use
gets a 403. On success it adds `X-Stargate-Key-Id`, `-Team` and `-Project` and
strips `Authorization`, so the provider never sees the caller's key. The access
log records those headers and receipt-ingest fills in the receipt's identity. A
403 is logged too, as a `blocked` receipt; a 401 has no key to attribute, so
like the dev gateway it gets no receipt. Key changes made through the API take
effect before the API responds.

Warden (`cmd/warden`) comes next, as an ext_proc filter. It gets the headers
and the whole body and runs the dev gateway's engine on them, so both paths
give the same verdict for the same request. A block-mode budget over its cap
gets a 429, and a blocking rule gets a 403 with the rule's message. Throttle
and warn budgets are admitted, with the trace saying so. A redaction rewrites
the message contents before anything goes upstream. A reroute rewrites the
model and sets an `X-Stargate-Backend` hint that the route matches on (Agent
Router strips it before the provider). Monitor-mode rules are recorded as
"would …". Warden's decision (verdict, rules, redactions, the budget and rule
steps) goes back as dynamic metadata. The access log carries it as
`stargate.policy`, and receipt-ingest lays it over the gateway's own fields, so
each receipt still comes from one record.

Warden has to run before Agent Router's own ext_proc. That processor reads the
model to route on and keeps a copy of the body, which it replays on retries,
model overrides and streamed requests. A redaction made after it would be
undone. By default Agent Router puts its filter first, but it goes after a
buffer filter that follows the other ext_procs, so `aigw/config.yaml` moves
the buffer filter behind Warden. Check the order after upgrading Agent Router:
`curl 'localhost:<envoy admin>/config_dump?resource=dynamic_listeners'` should
list `ext_authz → ext_proc/warden → buffer → ext_proc/aigateway → router`.
Warden also checks for itself: a request Agent Router has already processed
fails closed, with the reason in the receipt.

For request-path safety (§9.3), Warden reads a snapshot it reloads in the
background and never waits on the database. Evaluation has a 50ms deadline
(`-deadline`). A rule reached after the deadline applies its own fail mode. If
the engine hasn't answered at all, or panicked, the request fails closed when
any enforced rule does, and fails open otherwise. The kill switch is
`curl -XPOST 'localhost:8084/passthrough?on=true'` (or start with
`-passthrough`). It lets requests through unpoliced and marks each receipt that
way. `GET :8084/metrics` exports the snapshot age. The response path (inbound
detection, cutting streams, rehydration) isn't on this path yet, so nothing
here is `truncated`.

## Run it

```sh
cd server
make migrate                    # start both databases, migrate, seed the demo tenant
make backfill                   # optional: 7 days of synthetic history so charts have shape
make dev                        # fake upstream :8090, API :8080, gateway :8081, traffic ~0.8 rps
cd ../console && npm run dev:api   # console on the API (npm run dev stays on mock data)
```

To use Agent Router instead of the dev gateway, get the `aigw` binary for
your platform from the [releases](https://github.com/theagentrouter/agent-router/releases)
(v1.1.0 tested), then run `make dev-aigw`, or `make dev-aigw AIGW=/path/to/aigw`
if it's not on your PATH. The first run downloads Envoy into `~/.local/share/aigw`.

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
| `cmd/stargate-api serve` | REST + SSE on :8080, and Agent Router's ext_authz key check on :8082. Migrates and seeds on start. Also `migrate`, and `backfill -days N -per-day N`. |
| `cmd/devgateway` | `POST /v1/chat/completions` on :8081. Reloads config from the db every 5s. |
| `cmd/fake-openai` | `POST /{backend}/v1/chat/completions` on :8090, with streaming. Rejects a Stargate key with 401, so a leaked one shows up. |
| `cmd/receipt-ingest` | OTLP/gRPC logs receiver on :4317. Turns each Agent Router access-log record, with Warden's decision, into a receipt. |
| `cmd/warden` | Agent Router's ext_proc on :8083 (budgets, rules, redact, reroute). Admin on :8084: `/healthz`, `/metrics`, `POST /passthrough?on=`. |
| `aigw/config.yaml` | Agent Router config: the ext_authz key check, Warden's ext_proc and the filter order it needs, routes for every demo model plus Warden's backend hints, retries plus passive health checks for failover, the 50Mi buffer limit, and the access-log fields receipt-ingest reads. |
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
- `GET degradations` lists what the banner should show, worst first: Warden unreachable, its kill switch on, or its config cache stale (when `serve -warden` names Warden's admin URL, as `make dev-aigw` does), plus, from the last 15 minutes of receipts, requests Warden passed or refused because it couldn't decide, and backends failing at least 5% of 20+ requests.
- `GET stream/traffic?key&team&model&verdict&backend` is SSE. Each insert or settle sends a `receipt` event. Streamed requests arrive twice: first in flight, then settled.

Every mutation writes an `audit_log` row in the same transaction.

## Not yet (by design for this slice)

- **Auth.** There's no OIDC yet; every caller is `dev@localhost`. There's also no ETag/If-Match and no `dryRun`.
- **Mutations.** Only keys are writable. Backends, routes, rules and budgets are read-only over the API; the console still edits those in local state.
- **Rule engine.** It understands the demo rules' condition and action forms, with regex detectors. Warden runs the same engine. Message content has to be a plain string: a body with content parts (images) can't be inspected, so it gets the fail mode.
- **Routing.** Routes feed the fallback lists only. Key `allowedRegions` isn't enforced, and backend health is configured rather than probed.
- **Queries.** Rule fire counts and backend p50 read raw receipts. Continuous aggregates can't unnest jsonb or compute percentiles incrementally.
- **Response inspection.** It cuts a stream at an exfil URL, but a pattern split across chunks can leak its first part.
