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
    hidden.
  - Request totals follow the range picker.
- [ ] **Traffic.**
  - Remove "Simulate burst".
  - The provider filter comes from backends.
  - Scrolling loads older receipts with `?before=`.
  - The list follows the range picker.
- [ ] **Receipt drawer.**
  - Show the pricing snapshot (`costBasis`) and `policyMode`.
  - Export downloads the real receipt JSON.
  - Revealing content, the false-positive report and signing aren't
    connected yet.
- [ ] **Spend.**
  - The breakdown by team, key, model and provider comes from
    `receipts_daily`.
  - Ranges under a day come from `receipts_5m`.
  - Use the server's projection.
  - Surge and savings callouts: compute them or hide them.
- [ ] **Keys.**
  - Spend over 24h and an hourly sparkline per key.
  - Rotation status from `rotate_until` and the audit log. Traffic split
    per secret needs receipts to record which secret was used.
- [ ] **Activity.**
  - Before/after impact per change, from aggregates around its timestamp.
  - Traffic events from degradations and budgets.
- [ ] **Settings.**
  - The kill switch goes through a control-plane endpoint that calls
    Warden and writes an audit row.
  - Retention reflects the real policy.
  - The capture route comes from routes.
  - Warden snapshot age.
- [ ] **Models.**
  - Aliases from `model_aliases`, with 24h request counts.
  - Price history from `model_pricing`.

## 2. Writes

- [ ] Rules: create, publish, mode and fail mode, with audit rows. Warden
  picks up changes on its next snapshot.
- [ ] Routes and backends: apply. There's no reconciler yet, so "apply"
  writes config and the sync state says so.
- [ ] Budgets: create and edit.
- [ ] Aliases.
- [ ] Detector thresholds.
- [ ] Key rotation: extend the overlap, retire the old secret now.

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
- [ ] Model modalities and deprecation dates (new catalog columns).
- [ ] Gateway overhead p50 (not in receipts today).
