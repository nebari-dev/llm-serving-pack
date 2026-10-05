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
                  │  routing · retries · failover (tmp/aigw/config.yaml)
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
buffer filter that follows the other ext_procs, so `aigw/base.yaml` moves
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
way. `GET :8084/metrics` exports the snapshot age.

On the way back, Warden rehydrates: when a redacting rule says "rehydrate on
return", the values it replaced (kept in memory on the request's ext_proc
stream, never stored) go back into the reply, JSON or streamed, and the
receipt counts them. Inbound detection and cutting streams aren't on this
path yet, so nothing here is `truncated`.

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

### Restarting after a change

`make dev-aigw` doesn't rebuild on change. To pick up server changes without
stopping the rest of the stack:

```sh
make migrate                                  # if you added a migration
make restart                                  # stargate-api and Warden
make restart WHAT="api warden ingest aigw" AIGW=~/bin/aigw   # everything but the upstream and traffic
```

`scripts/restart.sh` builds into `bin/`, finds each process by its listening
port (api :8080, warden :8083, ingest :4317, aigw :1975), stops it by pid and
starts the new build in the background, logging to `tmp/<name>.log`. Restart
`ingest` after changing what it reads from the access log.

aigw runs `tmp/aigw/config.yaml` (gitignored): `aigw/base.yaml`, the
infrastructure, plus the routing compiled from Postgres (`internal/routing`).
`restart.sh aigw` writes it with `stargate-api routing write` the first time.
After that the console's Routing page rewrites it: route edits save to
Postgres, and "Apply" diffs the compiled routing against the file, writes it
and runs `restart.sh aigw`, putting the old file back if aigw doesn't answer
again. After editing `aigw/base.yaml`, delete `tmp/aigw/config.yaml` and
restart `aigw`, or apply from the console. The test stack does the same with
`tmp/aigw-test/config.yaml` and `scripts/test-stack.sh aigw`, which recreates
the container (`docker restart` would keep its old environment).

Provider keys set from the console go to `tmp/aigw/provider-keys.env`
(owner-only, `KEY=value` lines, gitignored; `-provider-keys` to move it), never
to Postgres, which keeps the reference and the key's first characters.
`restart.sh aigw` loads it after `server/.env`; the test stack merges
`tmp/aigw-test/provider-keys.env` over `.env` into the container's
`--env-file`. A replaced key is a pending change until routing is applied.

Never stop these with `pkill -f`: `make dev-aigw` runs everything under one
shell whose command line matches every command, and its `trap 'kill 0'`
takes the whole stack down. A restarted process no longer belongs to that
shell, so Ctrl-C on `make dev-aigw` leaves it running; stop it by port, e.g.
`kill $(lsof -tiTCP:8080 -sTCP:LISTEN)`.

## Commands

| | |
|---|---|
| `cmd/stargate-api serve` | REST + SSE on :8080, and Agent Router's ext_authz key check on :8082. Migrates and seeds on start. With `-aigw-config` and `-aigw-restart`, applies routing to aigw. Also `migrate`, `backfill -days N -per-day N`, and `routing write -o path`. |
| `cmd/devgateway` | `POST /v1/chat/completions` on :8081. Reloads config from the db every 5s. |
| `cmd/fake-openai` | `POST /{backend}/v1/chat/completions` and `GET /{backend}/v1/models` on :8090, with streaming. Rejects a Stargate key with 401, so a leaked one shows up. The `keyed` backend wants provider key `fakellm.KeyedKey` (401 otherwise) and echoes `keyed-echo`. |
| `cmd/receipt-ingest` | OTLP/gRPC logs receiver on :4317. Turns each Agent Router access-log record, with Warden's decision, into a receipt. |
| `cmd/warden` | Agent Router's ext_proc on :8083 (budgets, rules, redact, reroute). Admin on :8084: `/healthz`, `/metrics`, `POST /passthrough?on=`, and `POST /reload`, which the API calls after every config write. |
| `aigw/base.yaml` | Agent Router's infrastructure config: the ext_authz key check, Warden's ext_proc and the filter order it needs, retries plus passive health checks for failover, the 50Mi buffer limit, and the access-log fields receipt-ingest reads. Routing (the AIGatewayRoute with Warden's backend hints, and each backend) is compiled onto it from Postgres. |
| `cmd/trafficgen` | Poisson traffic at `-rps`, with the mockup's mix of keys, PII, secrets and EU requests. |

DB URLs come from `STARGATE_CONFIG_DB` and `STARGATE_RECEIPTS_DB`. The
defaults match `compose.yaml`.

## API

All paths are under `/api/v1/{tenant}`. JSON field names match
`console/src/data/mock.ts`.

- `GET teams | models | backends | routes | keys | budgets | rules | detectors | changes`
- `GET keys` adds each key's `spend24hUsd` and `hourly24h` (24 rolling hourly request bins from `receipts_5m` that sum to `requests24h`), and for a rotating key `rotation`: `endsAt` from `rotate_until`, `startedAt`/`startedBy` from its latest "Rotated key" audit row, each null when not recorded.
- `POST keys` returns `{key, secret}`; the secret is shown once and only its sha256 is stored.
- `POST keys/{id}/revoke`
- `POST keys/{id}/rotate` with `{overlapHours}`. Both secrets work until the overlap ends.
- `GET receipts?limit&before&since&range` plus repeatable filters `key` (id), `team`, `project`, `model` (requested or resolved), `verdict`, `provider`, `backend`, `reason` and `session`. Returns newest first; page with `before=<oldest ts>`. `range` starts the window on a 5-minute bucket.
- `GET receipts/count` with the same filters gives `{count, since}` from `receipts_5m`. `count` is null, with a `reason`, when a filter isn't in the aggregate (project, model, provider, reason, session) or the window isn't on 5-minute boundaries.
- `GET receipts/{id}`. Receipts include `costBasis`, the price row they were costed with, and `policyMode`.
- `GET series/traffic?range=15m|1h|6h|24h|7d|30d` gives verdict counts per bucket, from `receipts_5m`.
- `GET series/spend?days=30` gives daily spend by team, from `receipts_daily`.
- `GET activity?range=24h` lists the range's changes, each with `impact` (up to an hour of complete `receipts_5m` buckets either side: `before`/`after`, `bins`, `split`, `comparable`) and a computed `effect`/`effectTone`, plus traffic `events`: backends crossing the banner's failing threshold, and budgets crossing 80% and 100% of their cap.
- `GET spend?range=…&by=team|project|key|model|provider` returns the Spend page: breakdown rows for the range and the same span before it, a trend (5-minute to hourly buckets under a day, daily above), and the month-end projection with its basis. Whole UTC days come from `receipts_daily` and partial days from `receipts_5m`, so totals match `summary`'s. Provider comes from each receipt's backend, and project from the key's current project.
- `GET budgets` adds `currentUsd` (month to date, UTC) and `projectedUsd` (plus `trailingDailyUsd`, its basis: month to date + trailing 7-day average × days left).
- `GET degradations` lists what the banner should show, worst first: Warden unreachable, its kill switch on, or its config cache stale (when `serve -warden` names Warden's admin URL, as `make dev-aigw` does), plus, from the last 15 minutes of receipts, requests Warden passed or refused because it couldn't decide, and backends failing at least 5% of 20+ requests.
- `GET stream/traffic` is SSE, with the same filters as `receipts`. Each insert or settle sends a `receipt` event. Streamed requests arrive twice: first in flight, then settled. A connection that falls behind misses receipts, and a `dropped` event with `{count}` says how many.

Writes (the console doesn't call most of them yet):

- `PUT aliases/{alias}` with `{target}`, `DELETE aliases/{alias}`. A `*` only ends a pattern, and a pattern that would capture catalog models other than its target is refused. Overlapping patterns resolve by longest prefix.
- `POST budgets` with `{scopeType, scope, capUsd, onExceed}`; `PATCH budgets/{id}` with `{capUsd?, onExceed?}`; `DELETE budgets/{id}`. `?dryRun=true` on create and edit returns the budget with its spend, the active keys it would cover and `overCap`, and writes nothing. One budget per scope, monthly only. The gateway enforces every budget that covers a key (its team, project or name); the strictest over-cap one decides.
- `POST rules` creates an unpublished rule; `PUT rules/{id}/draft` and `DELETE rules/{id}/draft` edit or drop its pending draft; `POST rules/{id}/publish` with `{mode?, failMode?}` publishes the draft (or just the new mode or fail mode) as the next version, `?dryRun=true` to see the change; `POST rules/{id}/rollback` with `{version}`; `GET rules/{id}/versions`; `DELETE rules/{id}` for a rule that isn't live. The first publish defaults to monitor mode; published versions are immutable.
- `POST keys/{id}/rotation/extend` with `{hours}`, `POST keys/{id}/rotation/finish`. A rotating key in `GET keys` counts requests since the rotation started per secret (`oldSecretRequests`, `newSecretRequests`), from `receipts.secret_id`.
- Prices are per (model, backend), with LiteLLM as the default source (see `docs/backend-decisions.md` §1).
  - `GET pricing` lists every pair the tenant's backends serve: its rates and their sources, LiteLLM's last values, history, open proposals, and the sync's state.
  - `POST pricing/{model}/{backend}` (If-Match: the pair's `etag`) takes `{rates: {input|cachedInput|cacheWrite|output|reasoning: number | null}, effectiveFrom?}`. A number overrides the rate; null goes back to following LiteLLM.
  - `DELETE pricing/{model}/{backend}/{effectiveAt}` cancels a change that hasn't taken effect.
  - `PUT pricing/{model}/{backend}/source` with `{litellmKey}` sets which LiteLLM entry prices the pair (`""` for none).
  - `POST pricing/sync` runs the sync now.
  - `POST pricing/proposals/{id}/accept|dismiss`.
- The sync reads `-litellm-url` (default: LiteLLM's GitHub copy) once a day.
- Providers (backends), applied with routing:
  - `POST backends` with `{name, provider, region, baseUrl, models, apiKey?}`; `PUT backends/{name}` (If-Match) with the same minus name and key; `DELETE backends/{name}` (If-Match), refused (409) while a route sends to it. Models the catalog lacks are added to it with no price. Bedrock, Azure and Vertex are refused: they need cloud credentials.
  - `PUT backends/{name}/key` with `{apiKey}` stores the key in the key store, records its prefix, and tests it. No response ever carries a key.
  - `POST backends/test` with `{provider, baseUrl, apiKey?}` tests an unsaved provider (GET `{baseUrl}/models`; the key is used for that request only); `POST backends/{name}/test` tests a saved one with its stored key. Both return `{ok, status, models, error}`, the error in the provider's words with the key taken out.

Aliases, budgets and rules carry an `etag`. Updating or deleting one needs
`If-Match: <etag>` (428 without it; 409 with the current resource when it's
stale); creating an alias with `PUT` needs `If-None-Match: *`. Every mutation
writes an `audit_log` row in the same transaction.

## Not yet (by design for this slice)

- **Auth.** There's no OIDC yet; every caller is `dev@localhost`. There's also no ETag/If-Match and no `dryRun`.
- **Mutations.** Only keys are writable. Backends, routes, rules and budgets are read-only over the API; the console still edits those in local state.
- **Rule engine.** It understands the demo rules' condition and action forms, with regex detectors. Warden runs the same engine. Message content has to be a plain string: a body with content parts (images) can't be inspected, so it gets the fail mode.
- **Routing.** Routes feed the fallback lists only. Key `allowedRegions` isn't enforced, and backend health is configured rather than probed.
- **Queries.** Rule fire counts and backend p50 read raw receipts. Continuous aggregates can't unnest jsonb or compute percentiles incrementally.
- **Response inspection.** It cuts a stream at an exfil URL, but a pattern split across chunks can leak its first part.
