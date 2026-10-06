# Console: replacing fake data

Where the console, run against the control plane (`npm run dev:api`), still
shows values that don't come from it. The rule while this list is open: in api
mode the console never shows a made-up number or claims an action happened. A
piece without a backend shows as not connected, and its controls are disabled
with the reason. Mock mode (`npm run dev`) keeps its fixtures.

Inventory taken 2026-09-25 against `946c498`. Tick items as they land.

## 1. Read-only, from data we already have

- [x] **Shell.** Tenant name, environment, signed-in identity and roles
  (OIDC, or the dev actor in dev mode) with sign-out, notification bell (from `/degradations`), version footer
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
  - The stream reports receipts it dropped for a slow consumer, and samples
    above 40 matching requests a second (see Traffic sampling below).
  - Shared links carry the range.
  - Rows are virtualized and live updates batched: about 2,500 rows/min held
    p95 frame time at 17 ms, p99 at 33 ms.
- [x] **Receipt drawer.**
  - Show the pricing snapshot (`costBasis`) and `policyMode`.
  - Export downloads the real receipt JSON; Export signed, a signed zip
    (see Signed receipt export below).
  - Reveal content calls the server, which writes the audit row first. A
    redaction links to the false-positive queue on Guardrails → Detectors.
  - Print works. Related rows come from the server: same session, and the
    same key in the hour before.
- [x] **Spend.** One `GET /spend?range&by` serves the summary, the
  breakdown and the trend. Whole UTC days come from `receipts_daily` and
  partial days from `receipts_5m`, so totals match Overview's.
  - The breakdown by team, project, key, model and provider comes from the
    aggregates. `receipts_daily` now groups by backend, so provider is exact.
    Project rows are by project id (a key never changes project), labelled
    with the current name and the team, and drill to Traffic by id. Requests with no key
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
    throttle holds each key to 10 requests a minute (the budget's
    `throttlePerMinute`) and answers the rest 429 with Retry-After, warn
    admits and marks. The invented "since" is gone.
  - The surge callout is hidden (the spec has no surge rule) and shows as
    not connected. Savings is connected (2026-10-06, below).
  - CSV export downloads the breakdown; the close report is a PDF (below).
    Budgets are editable (see Writes).
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
  - "Projects" opens every team's projects: add, rename, delete (refused
    with the server's reason while a key is active or a budget names it).
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
  - Members come from `GET /members` (2026-10-06). Providers and the
    OTel/Argo CD integrations say they aren't connected yet. The kill-switch dialog's 24h blocked and
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

- [x] Policies (rules grouped as §5.2 has them): create, publish, mode and
  fail mode, with audit rows, on Guardrails (rules 2026-09-30; policies
  2026-10-06). Warden reloads before each write returns.
  - Backend: `GET`/`POST /policies`, `PUT`/`DELETE /policies/{id}/draft`,
    `POST /policies/{id}/publish` (mode, fail mode, `?dryRun=true` with
    reroute warnings), `POST /policies/{id}/rollback`,
    `GET /policies/{id}/versions`, `DELETE /policies/{id}`, and
    `GET /rules/vocabulary` (the entities, fields and route targets
    validation accepts, so the builder offers only those). A policy is an
    ordered list of rules with one mode and one fail mode, versioned as a
    unit; published versions are immutable; the first publish defaults to
    monitor mode. Config migration 045 made each existing rule a policy of
    its own, same id, name, mode, fail mode and versions (decisions §3).
  - Console (`pages/guardrails-live.tsx`): a policy list in evaluation
    order; open one to see and edit its rules in order (add, remove, move
    up/down, all in the draft). "New policy", then an explicit "Save draft"
    and "Discard draft"; "Publish…" shows the server's dry run (the policy
    diff and its reroute conflicts) and publishes as monitor, enforce or
    disabled; a disabled or unpublished policy can be deleted. Versions lists
    the selected policy's real history, diffs each version (all its rules)
    against the one before, and rolls back. A stale save shows both
    versions: keep theirs, or save mine over theirs. Links that carry
    `?rule=` (receipts, Overview, the palette) open the policy by id.
  - The builder matches the engine (user's choice, 2026-09-30): per rule, one
    list of conditions that must all match. Groups, "any of", regex and the
    response field aren't offered, and the page says why. A rule takes one or
    more actions, one of each kind (below). A redact action has a "Rehydrate
    on return" switch (off for a new one), which Warden honours (rehydration,
    below). The Replay pane runs the builder's rules (below).
  - Exit tests (api-mode, not yet run): the API test drafts a two-rule policy
    (redact and reroute in one rule, then a block), checks the refusals
    (block with others says what wins), the reroute warning against seeded
    eu-only, publishes it enforcing and sees `Rule <policy>/stop v2 blocks
    this request.` with both rules in the receipt, rolls back and deletes it.
    The UI test builds the same shape in the builder (the block-wins warning,
    moving a rule up), publishes it, sees `policy_blocked`, merges a stale
    draft, rolls back from Versions, disables and deletes it. The seeded
    rules list as one-rule policies with their versions.
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
  The savings analysis's "Review alias change" opens the alias's edit form
  with the cheaper target filled in (`/models?tab=aliases&edit=…&target=…`);
  nothing changes until Save.
- [x] Policy order (rules 2026-10-05; policies 2026-10-06). Move up/down in
  the policy list calls `PUT /policies/order {from, to}`: order decides
  outcomes (first block wins, last reroute wins), so a `from` that isn't the
  current order is a 409. One audit row ("Reordered policies", the moves),
  then Warden reloads. A policy's own rules are reordered in its draft and
  take effect when it's published.
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

- [x] Replay (§7.5.7) and per-route content capture (§9.2), 2026-10-06.
  - Capture is per route: `PUT /routes/{name}/capture {captureContent}`,
    If-Match, role security or admin ("capture"), audit row "Turned on/off
    content capture". It takes effect at Warden's reload, with no apply: the
    gateway's config doesn't change. Routing has a "Capture content" action
    per route with a confirm that says what is kept; the "Content capture on"
    marker shows on the route, on Traffic (a status line naming the routes),
    on Settings, and on each captured receipt. The backend-level marker is
    gone in api mode (the gateway path never used it).
  - Warden decides per request: the route the request takes as the caller
    sent it, matched among the routes of the last apply that took
    (`routing_applies.routes`, config migration 060; the desired routes
    before any apply recorded them), and whether that route captures now.
    On one that does, it keeps the prompt and the response as the caller got
    it (rehydrated), and writes them with every detector match replaced by
    its placeholder (`Detectors.Mask`): no detected value is stored. The
    write is off the request path (a queue of 1,024; full drops and counts
    in Warden's /healthz `captureDropped`), into `receipt_content` (receipts
    migration 009: a hypertable, 30-day retention), keyed by the receipt id.
    Refused requests are kept too, with no response. Responses are kept up
    to 1 MiB; a compressed one isn't kept, and says so.
    A stream that ends before the response does keeps what arrived and says
    so. A receipt whose content hasn't landed (or was dropped) reveals as a
    409 saying exactly that, not "wasn't captured". Drops are logged on the
    first and every 1,000th.
  - Warden's decision now carries `contentCaptured` and `dataRegion` (the
    `x-data-region` header); ingest stores both on the receipt
    (`receipts.data_region`; NULL before this).
  - Replay: `POST /policies/{id}/replay?window=1h|24h|7d|30d` (body: the
    builder's rules, unsaved; or none for the saved draft, else the live
    version). It loads a fresh snapshot, reads the window's receipts that
    reached policy evaluation (newest 10,000), and runs each through
    `Decision.runPolicies`, the loop Warden's `AdmitKey` runs, twice: the
    policies as they are, and with the draft in its place, enforced. A
    receipt with captured content replays exactly: each placeholder becomes a
    made-up value its detector matches (`Detectors.Unmask`). One without
    replays on metadata only: rules that read the prompt are left out on both
    sides, and the result names them. The result counts each kind apart
    (newly or no longer blocked, redacted, rerouted) and lists up to 50
    changed requests, each linking to its receipt and labelled Exact or
    Metadata only. 10,000 receipts replay in about 70 ms on the test stack.
  - Publish's dry run returns the same replay for the version it would
    publish (`replay`; `note` is gone), and the publish dialog shows it.
  - Exit tests (api-mode, run): capture through Agent Router with a masked
    reveal and its audit rows; replay exact on the captured request and
    metadata-only on the other, including a region rule and the dry run; the
    Routing toggle with its markers on Traffic and Settings; the Guardrails
    pane. 76/76.
- [x] Projects table (§5.2), so project budgets can exist before keys
  (decided 2026-10-05, decisions §2).
  - Config migration 011 makes each team's free-text project names on keys
    its projects (`projects(id, tenant_id, team_id, name)`, names unique per
    team), points keys at theirs by `project_id` (same team, enforced), names
    project budgets by project id, and drops `api_keys.project`. Receipts
    carried only the name until 2026-10-05 (below).
  - `GET /projects`; `POST /projects {team, name}` with an audit row ("Created
    project"), 409 on a name the team has. Budgets return `scopeName`, the
    project's name.
  - Console: the key form picks one of the team's projects or "New
    project…"; the budget form lists every project with its team and can add
    one, then cap it with no keys in it.
- [x] Projects by id, rename and delete (2026-10-05, decisions §2).
  - Receipts migration 008 adds `receipts.project_id` (set by the key check's
    `X-Stargate-Project-Id` header → access log → ingest, or Warden's engine);
    stargate-api backfills older receipts from their key's project, a UTC day
    at a time, once. The aggregates aren't rebuilt: they keep the key.
  - Spend's project rows, Traffic's project filter (list and stream) and
    "project is" rule conditions use the id; config migration 015 turned
    rule values that were names into the ids of every project with that
    name (what they matched before). The rule builder offers projects by
    name and team and stores the id.
  - Names are free text, unique per team ignoring case (migration 014).
    `PUT /projects/{id}` renames (If-Match, "Renamed project"); `DELETE`
    (If-Match) is a 409 saying what to do while the project has an active
    key or a budget, else "Deleted project" and it leaves `GET /projects`.
  - `POST /keys` takes `projectId`, or a name the team has; an unknown one is
    a 400 ("Create the project first, then the key"). The key form and
    onboarding create a "New project…" first, with its own audit row;
    onboarding defaults to the team's "onboarding" project, or makes it.
  - Exit tests (api-mode, not yet run): rename/delete over the API; two
    teams' same-named projects apart in receipts, Spend (API and page),
    Traffic and a rule; the key form's new project and Keys → Projects.
- [x] Throttle answers 429 with Retry-After; key-scoped budgets match by key
  ID (decided 2026-10-05, decisions §2).
  - Throttle is a per-key rate (spec §11 Phase 4, replacing the share
    refused): over a throttle cap each key gets 10 requests in any minute,
    counted in Warden's memory (`gateway.Throttle`); the next gets 429
    `budget_throttled` with `Retry-After` until the key's next slot. The
    trace says "admitted, n of 10" or "refused, next slot in Ns" (state
    `throttle`), and the receipt's verdict is `throttled`, so Traffic's
    filter, the Overview chart (lighter degraded hue) and Activity's blocked
    share tell it from a block. Block is unchanged.
  - Config migration 010 points key budgets at the key with their name (an
    active one first). The API returns `scopeName` (the key's name), and
    audit targets and traces use it. Activity's budget events match by id too.
  - Exit tests (api-mode, not yet run): a cent throttle cap answers
    `budget_throttled` with Retry-After within the minute, receipts say
    `throttled`, and Spend, Keys and the receipt say so; projects are
    created, audited and capped before a key exists, from the API and from
    Spend and the key form.
- [x] Policies as §5.2 has them: versioning and matching per policy
  (decided 2026-10-05, built 2026-10-06, config migration 045). See Writes
  above and decisions §3: one policy per existing rule, so nothing decides
  differently; policies evaluate in order, each policy's rules in theirs.
- [x] More than one action per rule, with §5.3's ordering (decided
  2026-10-05, built 2026-10-06). One of each kind per rule (redact and route
  to together); block stands alone, and the builder and the API say block
  wins. First block wins and short-circuits, redactions accumulate, the last
  reroute wins, with a conflict warning when authoring. Receipts list each
  rule's actions ("redact + route to"); Detectors show `policy/rule`.
- [x] Policy version history: versions, history and rollback on Guardrails
  (2026-09-30 per rule, migration 004; per policy 2026-10-06, migration 045,
  which kept each rule's history as its policy's).
- [x] **False-positive review queue and counts (2026-10-06, config migration
  051).** Guardrails → Detectors lists the last 7 days' detector hits (one
  row per entity per receipt: each redaction type, and the entity a policy
  block names; `GET /detectors/hits?entity&review=all`), unreviewed by
  default. A reviewer marks each one "False positive" or "Correct"
  (`POST /detectors/hits/verdict` with If-Match on the hit's review state);
  the verdict and its audit row (kind `Detector`) commit together, and a
  verdict can be changed later. The server refuses a verdict on an entity
  the receipt doesn't record. `GET /detectors` adds `falsePositives30d` and
  `confirmed30d` per detector (verdicts on receipts from the last 30 days),
  and the table shows "N of M reviewed".
  - What a reviewer sees, said on the page: receipts keep hashes, so the
    matched text is never there. Each row has the entity, match count,
    action and rule, key, team, model and time. Where the route captured
    content, that content has a placeholder in place of every detector
    match; the row links to the receipt, where revealing it is audited.
  - Monitor-mode matches still aren't hits: the receipt says "would redact"
    with no entity.
  - The receipt drawer links to the queue instead of saying it isn't
    connected.
- [x] Detector hit counts computed from receipts (2026-10-02). `GET /detectors`
  now lists the engine's own detectors (kind, pattern, placeholder), the live
  rules naming each one, and 24h redacted and blocked requests from receipts.
  The seeded `detectors` table is no longer read. In api mode the Detectors
  tab drops the thresholds, the browser-only regex tester and the fixture
  queue. Monitor-mode matches aren't counted: receipts record "would redact"
  with no entity.
- [x] **Custom entities: an entity registry the engine reads (2026-10-06,
  config migration 050).** A security user adds an entity on Guardrails →
  Detectors: name, regex, placeholder label, and examples it must and
  mustn't match (`POST /entities`, `PUT`/`DELETE /entities/{id}` with
  If-Match, audited, then Warden reloads). `?dryRun=true` checks it and tries
  it on a sample with the engine's own regex ("Test" in the form). Warden's
  snapshot loads them with everything else (`gateway.Detectors`), so rules
  name them in "contains entity" like built-ins; `GET /rules/vocabulary` and
  rule validation include them, and the builder's list reloads after a save.
  Detectors lists them with the built-ins, marked Custom, with Edit.
  - Pattern limits (`gateway.CheckPattern`): Go RE2 only (linear time, no
    backreferences or lookaround), at most 512 characters and 1,000 compiled
    instructions, and it can't match empty text (anchors or `\b` alone
    count as empty). Every built-in passes the same check.
  - Names: letters, digits, spaces, dashes, underscores, at most 40; unique
    ignoring case among built-ins and custom; can't change once made. Labels:
    capitals, digits, underscores; not another detector's. At most 20
    examples of each kind, 500 characters each; every save checks them.
  - Delete is refused (409, naming the policies) while a policy's rule names
    the entity: live in any mode, in its draft, or in one of its published
    versions, which a rollback would bring back (2026-10-06, with policies).
    A deleted policy's history doesn't count.
  - Exit tests (api-mode, written, not yet run): add an entity from the form
    after a server-side Test, publish a rule with it, see the provider get
    the placeholder and the receipt record it, see it on Detectors; edits
    need If-Match and can't rename; mark the hit a false positive on the
    queue and see the count and the audit row; then delete it.
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
    its error verbatim, key removed. Saving a key (create or replace) tests
    it first; one that fails is a 422 `key_test_failed` carrying the
    provider's error, and nothing is stored or audited. Migration 012 adds
    the key's prefix, when it was set and the last test; the key goes to a
    `routing.KeyStore`, staged until an apply: locally
    `tmp/aigw/provider-keys.pending.env`, which the apply promotes into the
    owner-only `tmp/aigw/provider-keys.env` aigw starts with (rolled back
    with the config if the gateway doesn't come back), so no other restart
    loads an unapplied key. The compiled Secret's `stargate.dev/key-version`
    annotation makes a replaced key a pending `key replaced` change (the
    test stack's apply recreates its container). Unknown models join the
    catalog with no price.
  - Console: Routing's "Add provider" (provider tiles, with Bedrock, Azure
    and Vertex disabled for want of cloud credentials; name, base URL,
    region, a password field for the key, Test connection, models to add
    from the test) and, in a backend's drawer, the key's prefix and last
    test, Edit provider, Replace key, Test connection and Delete provider.
    A key that fails its test shows "The key failed its connection test"
    with the provider's words, and the form stays open. Onboarding's
    "Connect a new provider" saves one, then "Route … to …" saves its route,
    then lists every pending routing change and applies them all ("Apply all
    N changes"): the applier applies one whole config, so it can't apply
    only that provider's. Settings → Providers lists each
    backend's key by prefix and its last test. Rotation reminders aren't
    built.
  - fake-openai: `GET /{backend}/v1/models`; a `keyed` backend that wants
    `fakellm.KeyedKey` or `KeyedKey2` (saying which in `X-Fake-Key`) and
    answers 401 otherwise; and a `keyed-anthropic` backend speaking
    Anthropic's native API (`GET /v1/models`, `POST /v1/messages`, x-api-key
    and anthropic-version) and, like Anthropic's OpenAI-compatible
    endpoint, chat completions with the same key as a bearer token, both
    echoing with `X-Fake-Received`. Every backend answers
    `POST /{backend}/v1/messages` (streamed as Anthropic's events), and a
    streamed chat completion's usage comes in a chunk of its own with no
    choices, as OpenAI sends it (Agent Router's Messages translation reads
    usage only from that).
  - Anthropic-style callers (2026-10-06, backend-decisions §6): the
    Anthropic SDK with base URL `<gateway>/anthropic` and a gateway key.
    An Anthropic provider compiles to a second AIServiceBackend,
    `<name>-native` (schema Anthropic, `AnthropicAPIKey`), and every rule
    to a pair, the copy for `x-stargate-api: anthropic`, which the key
    check sets. The key check reads `x-api-key` and refuses in Anthropic's
    error shape; Warden reads and rehydrates Messages bodies and streams;
    receipts name the backend, not its twin. Console: the Anthropic tile
    and its base URL say it serves both SDKs; the backend drawer's API row
    shows both APIs; onboarding shows the Anthropic SDK's base URL for an
    Anthropic backend.
  - Exit tests (api mode): an API test adds a provider at fake-openai's
    keyed backend (wrong key → 401 verbatim, right key → models; saving the
    wrong key → 422, nothing stored), saves it, checks no response carries
    the key, routes to it, applies, sees a receipt land on it, refuses a
    failing replacement key, replaces it with the second key (pending; the
    gateway sends key 1 until the apply, key 2 after), edits and deletes it
    (refused while routed); a UI test does the same on Routing; an
    onboarding test connects one, applies all N pending changes (listed,
    one not about it) and sends the first request through it; an Anthropic
    test checks the connection test is native and a failing key is refused.
    Another adds an Anthropic provider at keyed-anthropic and checks the
    plan adds `<name>-native` and its `AnthropicAPIKey` policy; an
    Anthropic-style request with the gateway key as x-api-key reaches the
    native fake (a Messages id); its receipt has the backend, the fake's
    tokens and the price set for the pair; a bad key and a disallowed model
    are refused in Anthropic's shape; a redact-and-rehydrate rule redacts a
    text block before the provider and restores it, whole and streamed; and
    an OpenAI-style call to the same provider still gets a chat completion.
    OpenAI-style callers to Anthropic's native API stay impossible: aigw
    v1.1.0 has no translator for it (backend-decisions §6).
- [x] Members and auth (OIDC), sign-out (2026-10-06, decisions §7).
  - Sign-in goes through the control plane (authorization code + PKCE,
    session in an HttpOnly cookie; `GET /api/auth/login`, `/callback`,
    `POST /api/auth/logout`), against Keycloak realm `nebari`. A 401
    sends the console to the login and back to the same page. Sign out
    ends the Keycloak session too.
  - Roles come from the token's `stargate-<role>` groups. Every write
    checks them (403 "Needs role …"); `GET /session` reports the user, roles
    and per-action `permissions`, and the console disables what the user
    can't do with "Needs role X". Key writes need the key's owner (new
    `owner` on keys) or admin.
  - Settings → Members lists `GET /members` (the `users` cache: who signed
    in, with their last roles) and links to Keycloak's groups page, where
    roles are assigned.
  - Dev mode (no `-oidc-issuer`, the default for `make dev`, the test stack
    and the api-mode suite) is unchanged: `dev@localhost`, owner, no
    sign-in. `make keycloak` + `make dev AUTH=oidc` runs a local Keycloak
    with a test user per role (server/README.md).
  - Not built: service accounts for CI, ownership transfer for keys.
- [ ] Routing reconciler: drift, adopt, provenance, reconcile events over
  SSE, and an applier for Kubernetes (held until llm-serving-pack's ownership
  questions are answered: docs/llm-serving-pack-survey.md), and a
  Kubernetes KeyStore writing Secrets. Cloud-credential providers (Bedrock,
  Azure, Vertex).
- [x] Signed receipt export, and revealing content with an audit row
  (2026-10-06, decisions §7).
  - `POST /receipts/export` (Traffic's filters and window) and
    `POST /receipts/{id}/export` (the drawer) return a zip: `receipts.jsonl`
    (a line describing the export, then each settled receipt, oldest first,
    as the API returns it), a detached Ed25519 signature over those exact
    bytes, the public key and a README with the `openssl` command. Over
    10,000 receipts is refused with "narrow the time range". The public key
    is `GET /receipts/signing-key`; verification steps are in
    `server/README.md`.
  - The key is made on first start in `tmp/receipt-signing.pem` (owner-only,
    gitignored), never returned.
  - Each export writes an audit row before the file goes out ("Exported
    receipts": who, the filter, the count, the file's SHA-256, the key id).
  - Content capture: Warden keeps content for routes that capture (see
    Replay, above); the dev gateway's engine (and `backfill`) still stores it
    for backends with `capture_content` (the seed's vllm-internal), unmasked
    demo text. So the drawer's Reveal content
    is live where content exists: `POST /receipts/{id}/reveal` writes
    "Revealed content" first, then returns it, and the drawer says who and
    when. Elsewhere it says "Not captured for this request".
  - These access rows are left out of `GET /changes` (Activity, Overview,
    the drawer's "most recent config change"): they changed nothing.
    `GET /changes?kind=Receipt` lists them; no page shows them yet.
  - Exit tests (api-mode, not yet run): the range export verifies against
    the published key and fails with a byte changed, with its audit row; the
    drawer's and Traffic's buttons download and audit; reveal is a 409 with
    no row when nothing was captured, and shows content after its row.
- [x] Traffic sampling (§7.5.3, 2026-10-06). Above 40 matching requests a
  second (2,400 a minute, just under the 2,500 a minute the table was
  measured to hold), the stream sends 1 in N, N a round number (2, 5, 10,
  20…) that brings it back under. It ends below 30 a second.
  - The rate is per connection, after its filters, so "Add a filter to see
    everything matching" is true. The header says "Sampling 1 in 20. Add a
    filter to see everything matching." with the measured rate. Only new live
    rows are sampled: counts and totals come from the database.
  - Paused, the pill says "N new (sampled 1 in 20)". After sampling ends,
    the list says it has gaps and offers Reload.
  - A request's in-flight and settled copies are kept or skipped together.
    Onboarding, which waits for a key's first request on the tab's stream,
    also asks the server while the stream is sampled.
  - Exit test (api-mode, not yet run): a `sampling` event shows the banner
    and the pill, and the count stays the database's.
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
- [x] Spend savings analysis (§7.5.5, 2026-10-06). `GET /spend/savings`
  reads the last 30 days of raw receipts (output length isn't in the
  aggregates, and rebuilding them would lose history older than 30 days;
  one grouped query, by key, requested/resolved model, price period, size
  bucket, short or not, priced or not). Method, stated on the page
  (decisions §1):
  - Counted: priced, served requests whose answer was at most 1,000 output
    tokens (reasoning included) and whose input + output fits the cheaper
    model's context. Unpriced ones are never a saving; they're counted apart.
  - The cheaper model is any same-`family` catalog model on a backend that
    offers it, at the same token counts, priced at its (model, backend) row
    in effect when each request started; the actual cost is the receipt's.
    A request it has no price for isn't counted. Best (model, backend) per
    group wins.
  - Groups are what an alias change moves: an alias's requests that ran on
    its target ("Review alias change" opens the pre-filled form on Models),
    or a key's requests for a model by name. Rerouted requests (policy,
    fallback) and revoked or expired keys' aren't counted.
  - It says which keys don't allow the cheaper model (they'd get 403
    `model_not_allowed`), and why each other request wasn't counted.
- [x] Spend close report as a PDF (2026-10-06). `GET
  /spend/close-report?month=YYYY-MM` (this month to date, or any past one)
  returns `application/pdf`: totals; spend by team, project, key and model
  from the aggregates; budgets against their caps (caps as configured now);
  unpriced requests (from raw receipts, so partial or "not known" past 30
  days, and said so); the price basis and the price rows in effect. Each
  export writes an audit row ("Exported close report", the month, kind
  `Export`) in the same transaction, so no file goes out unrecorded.
  Overview doesn't feature exports and Activity gives them no traffic
  effect. Spend has a month picker (default: last month) and "Download close
  report". Written by `internal/pdf`, a small writer of our own (decisions
  §7).
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
- [x] Price at the request's time (2026-10-05, spec §5.1). Ingest prices
  each receipt at the row in effect at its `start_time`, reading the rows
  from the db per OTLP batch, not at the 5-second config snapshot. The
  priced-later ticker uses the row in effect at the receipt's time if that
  prices it, else the first row set after it (then `pricedLater`).
- [x] Unpriced requests everywhere (2026-10-05, spec §5.1). Unpriced counts
  come from raw receipts (`UnpricedCells`), since the aggregates sum cost and
  can't tell no price from $0.
  - Spend: `unpriced` on the view, each row, the trend and the projection.
    The totals say "N requests have no price and aren't in this total" with
    a link to Models → Pricing. A row with no priced spend shows "No price";
    one with some shows "+ N no price". Cost / request is over priced
    requests. CSV has `unpriced_requests`, and `spend_usd` is "no price" when
    a row has nothing priced.
  - Overview: `current.unpriced` under the Spend number; `unpricedPairs` in
    Needs attention, each with "Set a price". The featured change's cost /
    request is over priced requests (null: "no price").
  - Keys: `unpriced24h`; the 24h spend and per-model cost read "No price"
    when nothing is priced. Budgets: `unpricedRequests` this month, not
    counted against the cap, noted on Spend, Keys, Overview and the edit
    dialog.
  - Activity: `costPerRequestUsd` is null with no priced requests, and
    `unpriced` per side; the effect doesn't count cost/request then.
  - Traffic and the receipt drawer already read "No price".
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
