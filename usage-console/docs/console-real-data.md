# Console: replacing fake data

Where the console, run against the control plane (`npm run dev:api`), still
shows values that don't come from it. The rule while this list is open: in api
mode the console never shows a made-up number or claims an action happened. A
piece without a backend shows as not connected, and its controls are disabled
with the reason. Mock mode (`npm run dev`) keeps its fixtures.

Inventory taken 2026-09-25 against `946c498`. Tick items as they land.

## 1. Read-only, from data we already have

- [x] **Shell.** Tenant name, environment, signed-in identity (the dev actor
  until OIDC), notification bell (from `/degradations`), version footer
  (console, control plane, Warden). The env switch becomes a label: one
  control plane serves one environment.
- [x] **Overview.** The status strip shows the age of the last receipt and
  the measured gateway overhead p50 (§3, 2026-10-05).
  - Spend today, and the deltas against the previous period.
  - Attention items (rule over baseline, failing backend, key anomaly).
  - Warden cache age.
  - The featured change, built from aggregates around the audit row, or
    hidden. The rows under it take Activity's computed effect; a change
    older than a week shows none.
  - Request totals follow the range picker.
- [x] **Traffic.**
  - Remove "Simulate burst".
  - The provider filter comes from backends.
  - Scrolling loads older receipts with `?before=`.
  - The list follows the range picker.
  - Filters run on the server, for the list and the stream. The matching
    count comes from `receipts_5m` when the filters allow, and otherwise the
    page says "N loaded".
  - The stream reports receipts it dropped for a slow consumer.
  - Shared links carry the range.
  - Rows are virtualized and live updates batched: about 2,500 rows/min held
    p95 frame time at 17 ms, p99 at 33 ms.
- [x] **Receipt drawer.**
  - Show the pricing snapshot (`costBasis`) and `policyMode`.
  - Export downloads the real receipt JSON.
  - Revealing content, the false-positive report and signing aren't
    connected yet.
  - Print works. Related rows come from the server: same session, and the
    same key in the hour before.
- [x] **Spend.** One `GET /spend?range&by` serves the summary, the
  breakdown and the trend. Whole UTC days come from `receipts_daily` and
  partial days from `receipts_5m`, so totals match Overview's.
  - The breakdown by team, project, key, model and provider comes from the
    aggregates. `receipts_daily` now groups by backend, so provider is exact.
    Project comes from each key's current project. Requests with no key
    identity show as "Unattributed", and requests refused before routing
    show as "Not routed". Neither drills through, because Traffic has no
    filter for them.
  - p50 latency is hidden in api mode, since the aggregates don't carry it.
  - The trend follows the range: 5- and 15-minute and hourly bars from
    `receipts_5m`, daily from `receipts_daily`. Sub-day bars drill to
    Traffic with `?since&until`.
  - Projection: month to date plus the trailing 7-day average × days left in
    the UTC month, basis stated. Budgets use the same basis.
  - Budget enforcement words say only what the gateway does: block refuses,
    throttle refuses a share of requests with 429 and Retry-After (stated,
    from the budget's spend), warn admits and marks. The invented "since" and
    rate are gone.
  - The surge callout is hidden (the spec has no surge rule), and so is
    savings, which needs per-request output length and alias writes. Both
    show as not connected.
  - CSV export downloads the breakdown. PDF is disabled, with the reason
    given. Budgets are editable (see Writes).
- [x] **Traffic streams.** Each api-mode Traffic tab held two SSE
  connections (the global receipt stream and Traffic's filtered one), so
  three tabs used up Chrome's 6-per-host HTTP/1.1 limit behind the Vite proxy,
  and later requests hung. Now a tab holds one: `receiptStream` owns it, and
  Traffic narrows it to its filters while open (still filtered by the server,
  §6), then widens it again on the way out.
  - Live rows outside the Traffic window are dropped. The stream ignores the
    window, so `?day=` today or a Spend bar's `?until=` kept taking rows
    after the window closed.
  - Still one connection per tab: six api-mode tabs of any page reach the
    limit again. Serving the console over HTTP/2 lifts it.
- [x] **Keys.** `GET /keys` carries each key's 24h spend and hourly
  requests, and the list re-reads it every 30 s.
  - Spend over 24h and an hourly sparkline per key, both from `receipts_5m`
    over the same rolling 24h as the request count. The sparkline's bins
    sum to that count, and spend matches Spend's key breakdown.
  - Rotation status: the window's end comes from `rotate_until`, and its
    start and who started it come from the key's latest "Rotated key" audit
    row. Each is marked as not recorded when missing. The chip shows time
    left, not a share. The traffic split per secret isn't shown, because
    receipts don't record which secret was used. "Extend overlap" and
    "Retire old secret now" are disabled until the key-rotation writes land.
  - Budget wording matches Spend's. The invented "rotation reminders every
    90 days" is gone.
- [x] **Activity.** One `GET /activity?range` serves the changes in the
  range and the traffic events, all from `receipts_5m`.
  - Before/after per change, across the tenant: up to an hour of complete
    5-minute buckets either side (the change's own bucket counts as after,
    and the bucket still filling is left out). It carries requests,
    cost per served request, 5xx/429 rate and blocked + redacted share, and
    the sparkline bins sum to the same counts. There's no p50, since the
    aggregates don't carry it.
  - The effect is computed, not the audit row's stored text: Regressed if any
    metric rose 5% or more, Improved if something fell that much and nothing
    rose, otherwise Informational. Under 20 requests either side says "too
    little traffic". A metric that was 0 before isn't counted. The Effect
    filter uses the same tones.
  - Traffic events: a backend crossing the banner's failing threshold (5% of
    at least 20 requests over 15 minutes, and at least 3 failures) either
    way. An episode ends only after a full 15 minutes under the threshold,
    so dips in and out read as one. Plus a budget's month
    spend crossing 80% and 100% of today's cap, pinned to its 5-minute
    bucket. Policy-mode windows aren't included (not in the aggregates).
- [x] **Settings.**
  - The kill switch goes through `POST /warden/passthrough`, which writes
    the audit row and calls Warden in one transaction: no row unless Warden
    took the change, and asking for the current state is a no-op. Warden
    holds the flag in memory, so a restart turns it back off; the page says
    so.
  - Retention reflects the real policy: `GET /retention` reads Timescale's
    jobs. Migration 004 adds the §4.6 30-day drop on raw receipts; the
    aggregates have no drop policy, so the cold tier reads "Never dropped"
    (the mockup's "7 years" isn't a policy anywhere).
  - The capture route comes from routes.
  - Warden snapshot age, from `/session`.
  - Providers, members, and the OTel/Argo CD/Keycloak integrations say
    they aren't connected yet. The kill-switch dialog's 24h blocked and
    redacted counts come from `/summary`.
- [x] **Models.**
  - Aliases from `model_aliases`, with 24h request counts (GET /aliases).
    Counts come from raw receipts by `requested_model`, matched with the
    gateway's own alias rule, so requests a policy or fallback later
    rerouted still count. Conditions and owner aren't in the schema: "always"
    and "Not recorded". New alias stays disabled until alias writes.
  - Prices per (model, backend) from GET /pricing (2026-10-05, decisions §1).
    Each rate shows its source: LiteLLM, override or seed. A pair without a
    price shows "No price". History has one change per rate that differs
    between consecutive rows, including ended prices. CSV export is disabled.
  - Catalog modalities and deprecation dates show as not connected (§3).

## 2. Writes

The backend for most of these landed 2026-09-29. Budgets and rules have their
console forms (2026-09-30); the rest stay disabled in api mode until theirs land. Every write below has an audit
row in the same transaction and takes `If-Match` (409 with the current row
when stale; 428 without it on an update or delete). Open questions are in
`docs/backend-decisions.md`.

- [x] Rules: create, publish, mode and fail mode, with audit rows, on
  Guardrails (2026-09-30). Warden reloads before each write returns.
  - Backend: `POST /rules`, `PUT`/`DELETE /rules/{id}/draft`,
    `POST /rules/{id}/publish` (mode, fail mode, `?dryRun=true`),
    `POST /rules/{id}/rollback`, `GET /rules/{id}/versions`,
    `DELETE /rules/{id}`, and `GET /rules/vocabulary` (the entities, fields
    and route targets validation accepts, so the builder offers only those).
    Published versions are immutable; the first publish defaults to monitor
    mode.
  - Console (`pages/guardrails-live.tsx`): "New rule", then an explicit
    "Save draft" and "Discard draft"; "Publish…" shows the server's dry run
    and publishes as monitor, enforce or disabled; a disabled or unpublished
    rule can be deleted. Versions lists the selected rule's real history,
    diffs each version against the one before, and rolls back. A stale save
    shows both versions: keep theirs, or save mine over theirs.
  - The builder matches the engine (user's choice, 2026-09-30): one list of
    conditions that must all match, and one action. Groups, "any of", regex,
    the response field and a second action aren't offered, and the page says
    why. A redact action has a "Rehydrate on return" switch (off for a new
    one), which Warden honours (rehydration, below). Replay says it isn't
    connected. Reordering isn't connected.
  - Exit test: the api-mode suite builds a rule in the builder, publishes it
    enforcing, sees the gateway refuse with `policy_blocked`, merges a stale
    draft, rolls back from Versions, disables and deletes it.
- [x] Routes and backends: desired state, the route editor, and apply to
  the local gateway (2026-10-05; replaces the read-only decision of
  2026-09-30, decisions §6).
  - Backend: config migration 009 rebuilds `routes` in the gateway's shape
    (match `{models, headers}`, targets `{backend, model?, weight?}`, an
    ordered fallback) from what `aigw/config.yaml` had by hand, adds
    endpoint columns to `backends`, and `routing_applies`. The seeded routes
    (default, cheap-summarize, eu-private, research-frontier) are gone.
    `internal/routing` compiles routes and backends to the AIGatewayRoute,
    Backend, AIServiceBackend and provider-key resources, diffs them against
    the running config, and applies through an `Applier`. The local one
    writes `tmp/aigw/config.yaml` (`aigw/base.yaml` + routing) and runs
    `restart.sh aigw`, rolling back if aigw doesn't answer within 90s.
    `GET/POST /routes`, `PUT/DELETE /routes/{name}` (audit rows, If-Match,
    `?dryRun=true` returns the rule), `GET /routing` (diff, plan etag,
    export YAML, last apply), `POST /routing/apply` (If-Match: the plan's
    etag; 502 with aigw's error after a rollback). Sync is `synced`,
    `pending` or `failed` per route and backend, and `no_endpoint` for a
    backend with no endpoint. Compiling the seed reproduces the hand-written
    config except Warden's reroute hint for `local` and `openrouter`, which
    it lacked.
  - Console: api mode renders `pages/routing-live.tsx`. Routes list with
    sync, a route editor (models, header conditions, weighted targets with
    model overrides, ordered fallback, Preview YAML from a dry run, stale
    edit refused with "Load the current version"), delete, an apply bar
    with the CRD diff before applying, and Export YAML. Backends show
    endpoint, sync and their generated YAML, and are edited from their
    drawer (see Provider credentials below).
  - Exit test: the api-mode suite creates a route matching `x-stargate-team`,
    applies it to the test gateway (Docker), and sees a support key's
    gpt-5.5 land on vllm-internal as llama-3.3-70b; the UI test creates,
    applies, edits against a stale etag and deletes a route.
- [x] Budgets: create, edit and delete on Spend (2026-09-30). The gateway
  enforces every budget whose scope covers a key (its team, project or the
  key itself); the strictest over-cap one decides. Keys no longer name a
  budget (`budget_id` dropped).
  - Backend: `POST /budgets`, `PATCH`/`DELETE /budgets/{id}`, with
    `?dryRun=true`.
  - Console: "Add budget" and per-row edit and delete. Scope is team,
    project or key, picked by name and sent by id (key and project, decisions
    §2); the scope is fixed once made. "New project…" adds a project there. What happens at the cap has no default. The
    form shows the dry run as you type: keys covered, spend this month, and
    a warning when the budget is already over the new cap. A stale edit or
    delete (409) shows both versions and asks: keep theirs, or save mine
    over theirs (§6).
  - Exit test (§11): the api-mode suite sets a $0.01 block cap through the
    form, drives the gateway until Warden refuses with `budget_exceeded`,
    and finds the blocked receipt with the budget in its trace.
- [x] Aliases (2026-10-05). New alias, Edit (retarget) and Delete on Models
  call `PUT`/`DELETE /aliases/{alias}` with If-None-Match / If-Match; the
  table shows a write's result at once so a follow-up carries the new etag.
  Saving the savings analysis's draft alias changes waits on that analysis
  (§3).
- [x] Rule order (2026-10-05). Move up/down on Guardrails calls
  `PUT /rules/order {from, to}`: order decides outcomes (first block wins,
  last reroute wins), so a `from` that isn't the current order is a 409. One
  audit row ("Reordered rules", the moves), then Warden reloads.
- [ ] Detector thresholds. On hold (decided 2026-09-30): the regex
  detectors have no confidence to threshold (decisions §6).
- [x] Key rotation: extend the overlap, retire the old secret now.
  - `POST /keys/{id}/rotation/extend` and `/finish`, from the rotation
    dialog on Keys. Extend adds a fixed 24h, and is disabled with its reason
    when that would end the overlap more than 7 days from now (the server's
    limit). Retire asks first and states how many requests have used the
    old secret since the rotation started (user's choice, after
    revocation's blast radius; the mockup retired on one click with an
    invented "39%").
- [x] Receipts record which secret (old or new) authenticated a request, so
  rotation can show traffic moving between them (§7.5.8).
  - `receipts.secret_id`, and a rotating key's
    `oldSecretRequests`/`newSecretRequests` in `GET /keys`. The rotation
    panel shows the counts and the new secret's share since the rotation
    started (not over 24h, and without the mockup's per-actor list or a
    made-up new-secret prefix), plus any requests that didn't record a
    secret. Without a recorded start, the split is marked as not recorded.
- [x] Model prices (2026-10-05). The Edit dialog on Models → Pricing sets
  each rate to follow LiteLLM or to an override, now or scheduled, plus the
  pair's LiteLLM entry. Endpoints:
  - `POST /pricing/{model}/{backend}` sets or schedules rates (If-Match).
  - `DELETE …/{effectiveAt}` cancels a scheduled change.
  - `PUT …/source` sets the LiteLLM entry. The key must be in the file, and
    a sync runs at once.
  - Proposals: accept or dismiss.

## 2b. Backends seen, not seeded (2026-10-05)

- [x] `GET /backends` health, p50 and error rate come from receipts, never the
  seed: health from the banner's 15-minute window (idle with no requests,
  down when every request of at least 3 failed, degraded on the banner's
  failing rule or when every one of fewer failed), p50 and errors from the
  last hour, with `requests1h`. Errors count every upstream failure,
  auth included. Overview's strip and Routing poll it. The engine's own
  reroute still reads the seeded health column.
- [x] Real upstreams next to fake-openai: `local` (any OpenAI-compatible
  server on this machine; Docker Model Runner by default, serving `smollm2`)
  and `openrouter` (`gpt-4o-mini`, priced from LiteLLM's
  `openrouter/openai/gpt-4o-mini`), with key `local-dev` (k8) allowed both.
  `OPENROUTER_API_KEY` and the local server's port/prefix/model go in
  `server/.env` (see `.env.example`). A receipt keeps the catalog model when
  the upstream names its own (a GGUF path, `openai/gpt-4o-mini`) and the
  trace says what the upstream called it.

## 3. New systems

- [ ] Replay: run Warden's evaluator over stored receipts, to see what a new
  rule would have caught. Needs content capture, or a replay over
  hashes/metadata only. Until it exists, publish's dry run can't tell you
  much.
- [x] Projects table (§5.2), so project budgets can exist before keys
  (decided 2026-10-05, decisions §2).
  - Config migration 011 makes each team's free-text project names on keys
    its projects (`projects(id, tenant_id, team_id, name)`, names unique per
    team), points keys at theirs by `project_id` (same team, enforced), names
    project budgets by project id, and drops `api_keys.project`. Receipts
    still carry the project's name, so Spend, Traffic and rules are as before.
  - `GET /projects`; `POST /projects {team, name}` with an audit row ("Created
    project"), 409 on a name the team has. `POST /keys` still takes the
    project by name within the key's team, and creates a missing one (with
    its own audit row). Budgets return `scopeName`, the project's name.
  - Console: the key form picks one of the team's projects or "New
    project…"; the budget form lists every project with its team and can add
    one, then cap it with no keys in it.
- [x] Throttle answers 429 with Retry-After; key-scoped budgets match by key
  ID (decided 2026-10-05, decisions §2).
  - Over a throttle cap Warden refuses `0.5 + 2.5 × (spent − cap)/cap` of
    requests (half at the cap, all from 120% of it) with 429
    `budget_throttled` and `Retry-After: 5`; the rest are admitted and the
    trace says the share. Receipts show `blocked` with that code. Block is
    unchanged. The draw is the engine's injected rand, so tests fix it.
  - Config migration 010 points key budgets at the key with their name (an
    active one first). The API returns `scopeName` (the key's name), and
    audit targets and traces use it. Activity's budget events match by id too.
  - Exit tests (api-mode, not yet run): a cent throttle cap answers
    `budget_throttled` with Retry-After 5 and Spend says so; projects are
    created, audited and capped before a key exists, from the API and from
    Spend and the key form.
- [ ] Policies as §5.2 has them: versioning and matching per policy
  (decided 2026-10-05).
- [ ] More than one action per rule, with §5.3's ordering (decided
  2026-10-05, later).
- [ ] Rule version history (policy_rules keeps only a version number).
- [ ] False-positive review queue.
- [x] Detector hit counts computed from receipts (2026-10-02). `GET /detectors`
  now lists the engine's own detectors (kind, pattern, placeholder), the live
  rules naming each one, and 24h redacted and blocked requests from receipts.
  The seeded `detectors` table is no longer read. In api mode the Detectors
  tab drops the thresholds, the browser-only regex tester and the fixture
  queue, and says custom entities and false-positive review aren't connected.
  Monitor-mode matches aren't counted: receipts record "would redact" with no
  entity.
- [ ] False-positive counts, and custom detector patterns (an entity registry
  the engine reads).
- [x] Provider credentials: add a provider, list, replace, test connection
  (2026-10-05, decisions §6).
  - Onboarding is real in api mode (2026-10-05): it lists `/backends` with
    observed health, creates a real key for the chosen backend's models,
    shows the gateway URL from `/session`, and waits for that key's first
    receipt. "Send a test request for me" is `POST /gateway/test`: one small
    request through the gateway, in its own `X-Session-Id` (Envoy replaces a
    caller's x-request-id, which receipt ids derive from).
  - Server: `POST/PUT/DELETE /backends` (audit rows, If-Match on edit and
    delete, delete refused while routed), `PUT /backends/{name}/key`,
    `POST /backends/test` (unsaved: the key in the body only) and
    `POST /backends/{name}/test`, which list the provider's models or give
    its error verbatim, key removed. Migration 012 adds the key's prefix,
    when it was set and the last test; the key goes to a `routing.KeyStore`
    (locally the owner-only `tmp/aigw/provider-keys.env` aigw starts with).
    The compiled Secret's `stargate.dev/key-version` annotation makes a
    replaced key a pending `key replaced` change, applied by a restart (the
    test stack recreates its container). Unknown models join the catalog
    with no price.
  - Console: Routing's "Add provider" (provider tiles, with Bedrock, Azure
    and Vertex disabled for want of cloud credentials; name, base URL,
    region, a password field for the key, Test connection, models to add
    from the test) and, in a backend's drawer, the key's prefix and last
    test, Edit provider, Replace key, Test connection and Delete provider.
    Onboarding's "Connect a new provider" saves one, routes its models to
    it and applies before the key step. Settings → Providers lists each
    backend's key by prefix and its last test. Rotation reminders aren't
    built.
  - fake-openai: `GET /{backend}/v1/models`, and a `keyed` backend that
    wants `fakellm.KeyedKey` and answers 401 otherwise.
  - Exit tests (api mode): an API test adds a provider at fake-openai's
    keyed backend (wrong key → 401 verbatim, right key → models), saves it,
    checks no response carries the key, routes to it, applies, sees a
    receipt land on it, replaces the key (pending, applied → 401, right key
    → 200), edits and deletes it (refused while routed); a UI test does the
    same on Routing; an onboarding test connects one and sends the first
    request through it.
- [ ] Members and auth (OIDC), sign-out.
- [ ] Routing reconciler: drift, adopt, provenance, reconcile events over
  SSE, and an applier for Kubernetes (held until llm-serving-pack's ownership
  questions are answered: docs/llm-serving-pack-survey.md), and a
  Kubernetes KeyStore writing Secrets. Cloud-credential providers (Bedrock,
  Azure, Vertex). Content capture per route compiles to nothing yet.
- [ ] Signed receipt export, and revealing content with an audit row.
- [ ] Traffic sampling (§7.5.3): above a rate threshold the stream sends 1 in
  N, with the rate in the header. Today the stream only counts and reports
  what it dropped.
- [x] Model modalities and deprecation dates (2026-10-05). The daily
  LiteLLM sync also saves, per priced-from key, the input modalities
  (`supports_vision`/`_audio_input`/`_pdf_input`, transcription = audio
  only) and `deprecation_date` (`litellm_facts`). `GET /models` adds the
  union of modalities over the model's backends and each backend's
  retirement date; Models → Catalog flags dates within 30 days. A model no
  backend has an entry for shows Unknown (smollm2, llama-3.3-70b,
  claude-opus-4-1 on anthropic-prod).
- [x] Gateway overhead p50 (2026-10-05). The access log carries
  `%COMMON_DURATION(DS_RX_END:US_TX_BEG:us)%` (whole request received to
  first byte upstream: key check, Warden, Agent Router) into
  `receipts.overhead_us`; `GET /gateway/overhead` gives the last hour's p50
  and p95 against spec G6's 10ms, shown on the Overview status strip.
- [ ] Spend savings analysis (§7.5.5): requests a cheaper same-family model
  would have served. Needs output length per request, or an aggregate of it.
- [ ] Spend close report as a PDF, with an audit row for each export.
- [x] Pricing sync (2026-10-05, decisions §1).
  - stargate-api reads LiteLLM's price file daily, retrying hourly after a
    failure; Sync now runs it on demand. Rates nobody overrode apply as new
    effective-dated rows, audited as "LiteLLM sync".
  - A LiteLLM move on an overridden rate becomes a proposal.
  - Seed prices were retired at the first sync. Pairs without a LiteLLM
    entry (llama on vllm-internal, opus 4.1 on anthropic-prod) have no price.
  - Receipts for an unpriced pair have no cost, and Spend counts them. Once
    the pair gets a price, a minute ticker costs them at it, marked
    `pricedLater`.
  - Cache writes count on receipts (Agent Router's `CacheCreationInputToken`)
    and bill at their own rate.
- [x] Redaction rehydration (§4.5 step 5, 2026-10-05, decisions §3).
  - Engine: placeholders are numbered per request, not per message, and the
    same value always gets the same one; a placeholder the caller typed is
    skipped. Redactions by a rule whose action says "rehydrate on return"
    go into a vault; any other redaction ("no rehydrate", or nothing said)
    keeps its placeholders in the response.
  - Warden: the vault lives on the request's ext_proc stream, in memory,
    and goes with it (no table). `aigw/base.yaml` now sends Warden the
    response (`response.body: Streamed`, `allowModeOverride`); with an empty
    vault Warden tells Envoy to skip the body. It sees the reply after
    Agent Router's translation, so it's OpenAI's schema: a JSON body is held
    whole and its choices' strings restored; SSE events go out as they
    complete, and a placeholder streamed over several deltas is held back
    until it's whole (content and tool-call arguments). content-length is
    dropped. A compressed or non-chat body isn't touched, and the receipt
    says why.
  - Receipts: each redaction carries `rehydrated` (a count, inside the
    existing JSON column, no migration), and the trace ends with
    "Placeholders rehydrated". The receipt drawer shows the count.
  - devgateway (the dev stand-in) doesn't rehydrate.
  - Exit test: the api-mode suite sends an email through the gateway to
    fake-openai in echo mode (`X-Fake-Echo`): the provider receives
    `[EMAIL_1]`, the caller gets the address back, JSON and streamed, the
    receipts count it; with the rule's rehydrate off, the placeholder stays.
