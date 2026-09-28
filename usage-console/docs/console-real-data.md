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
- [x] **Overview.** Gateway overhead isn't measured, so the status strip
  shows the age of the last receipt instead.
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
  - Budget enforcement words say only what the gateway does. Throttle isn't
    enforced (requests are admitted and marked), and the invented "since" and
    rate are gone.
  - The surge callout is hidden (the spec has no surge rule), and so is
    savings, which needs per-request output length and alias writes. Both
    show as not connected.
  - CSV export downloads the breakdown. PDF and "Add budget" are disabled,
    with the reason given.
- [ ] **Traffic streams.** Each api-mode Traffic tab holds two SSE
  connections (the global receipt stream and Traffic's filtered one), so
  three tabs use up Chrome's 6-per-host HTTP/1.1 limit behind the Vite proxy,
  and later requests hang. Share one stream per tab.
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
  - Budget wording matches Spend's (throttle isn't enforced). The invented
    "rotation reminders every 90 days" is gone.
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
  - Price history from `model_pricing` (GET /pricing): each model's current
    effective date, and one change per rate that differs between
    consecutive rows. The seed has none yet. Sources show "Seed price" until
    the pricing sync; CSV export is disabled.
  - Catalog modalities and deprecation dates show as not connected (§3).

## 2. Writes

- [ ] Rules: create, publish, mode and fail mode, with audit rows. Warden
  picks up changes on its next snapshot.
- [ ] Routes and backends: apply. There's no reconciler yet, so "apply"
  writes config and the sync state says so.
- [ ] Budgets: create and edit. Project-scoped budgets show spend on the
  Spend page, but the gateway only checks team and key budgets.
- [ ] Aliases, including saving the savings analysis's draft alias changes.
- [ ] Detector thresholds.
- [ ] Key rotation: extend the overlap, retire the old secret now.
- [ ] Receipts record which secret (old or new) authenticated a request, so
  rotation can show traffic moving between them (§7.5.8).

## 3. New systems

- [ ] Replay: run Warden's evaluator over stored receipts. Needs content
  capture, or a replay over hashes/metadata only.
- [ ] Rule version history (policy_rules keeps only a version number).
- [ ] False-positive review queue.
- [ ] Detector hit and false-positive counts computed from receipts, and
  custom detector patterns.
- [ ] Provider credentials: list, replace, test connection. Also onboarding.
- [ ] Members and auth (OIDC), sign-out.
- [ ] Routing reconciler: drift, adopt, reconcile events.
- [ ] Signed receipt export, and revealing content with an audit row.
- [ ] Traffic sampling (§7.5.3): above a rate threshold the stream sends 1 in
  N, with the rate in the header. Today the stream only counts and reports
  what it dropped.
- [ ] Model modalities and deprecation dates (new catalog columns).
- [ ] Gateway overhead p50 (not in receipts today).
- [ ] Spend savings analysis (§7.5.5): requests a cheaper same-family model
  would have served. Needs output length per request, or an aggregate of it.
- [ ] Spend close report as a PDF, with an audit row for each export.
- [ ] Pricing sync: keep `model_pricing` current from the providers' published
  prices, not the demo seed. A price change adds a new effective-dated row, so
  receipts keep the rate they were costed with. Today every cost is tokens ×
  seed prices from `internal/demo`.
