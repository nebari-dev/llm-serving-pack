# Backend: decisions and open questions

Written 2026-09-29 at the end of the backend writes pass (`7659a28` onward
on `backend-api`). Each item says what the code does today, what's open,
and a recommendation. Items marked **Decide** block work; the rest are
defaults I picked that you may want to change.

## 1. Pricing: where do prices come from?

**Today.** `model_pricing` holds effective-dated rows per model: input,
cached input, output, reasoning, per 1M tokens. The only rows are the demo
seed (`internal/demo`, effective 2026-01-01). receipt-ingest costs each
settled receipt at the row in effect when its snapshot loaded (reloaded
every 5s), and stores that row on the receipt as `cost_basis`, so later
price changes never reprice history. New in this pass:
`POST /pricing/{model}` adds the next row (now or scheduled) and
`DELETE /pricing/{model}/{effectiveAt}` cancels a scheduled one, both
audited. Any sync would write through the same path.

**Decide: the source of truth.** The options:

| Source | What it gives | Catch |
|---|---|---|
| Manual entry (the new endpoint, once the console has a form) | Exactly what you pay, including negotiated discounts | Someone has to notice provider changes |
| LiteLLM's `model_prices_and_context_window.json` (MIT, on GitHub) | Broad coverage, keyed per provider route (`bedrock/…`, `azure/…`), with `input_cost_per_token`, `output_cost_per_token`, `cache_read_input_token_cost`, `output_cost_per_reasoning_token` | Community-maintained, can lag or be wrong; list prices only |
| Provider pricing pages | Authoritative list prices | No machine-readable API from OpenAI or Anthropic that I know of; scraping is brittle |
| OpenRouter's models API | Prices per model | OpenRouter's resale price, not the provider's; I couldn't confirm the response shape from here |
| Cloud billing exports (AWS CUR, Azure cost exports) | What was actually billed | After the fact, not per request; good for month-end reconciliation only |
| Self-hosted (`vllm-internal`) | Nothing: there's no list price | Needs an internal chargeback rate, or $0 |

**Recommendation.** Manual entry is the authority, and a daily job reads
LiteLLM's file and *proposes* changes. It never applies them: a proposal
becomes a scheduled row only when someone approves it. Contract rates and
self-hosted rates stay manual. That needs a small mapping table from our
catalog id plus backend to LiteLLM's key (for example `claude-sonnet-5` via
`bedrock-eu` maps to a `bedrock/...` key).

**Decide: schema gaps a real price list exposes.**
- **The same model costs different amounts on different backends**
  (Anthropic direct vs Bedrock EU). `model_pricing` is keyed by model only.
  Should a price be per (model, backend)?
- **Prompt-cache writes** (`cache_creation_input_token_cost`) have their own
  rate. Receipts don't count cache-write tokens, and the schema has no
  column for the rate.
- **Long-context tiers** (LiteLLM's `…_above_200k_tokens`), plus batch and
  priority tiers. There's one rate per token type today.
- **Who may change prices.** The catalog is shared by every tenant, so a
  change reprices everyone. There are no roles yet (see §7); it should be a
  platform-admin action.

Minor: ingest prices at snapshot load, not at the receipt's timestamp, so
for up to 5s after a change takes effect receipts can carry the old rate.
Pricing by the receipt's `ts` would fix it; say if it matters.

## 2. Budgets

Done: every budget whose scope covers a key is enforced (team, project,
key), and the strictest over-cap one decides. There's create/edit/delete
with dry runs, and a $0.01 cap set through the API stops real requests at
Agent Router (api-mode test).

- **Decided 2026-09-30: `api_keys.budget_id` is dropped** (migration 005).
  Keys don't name a budget; the key form lists the budgets that will cover
  the new key, and budgets are managed on Spend.
- **Decide: projects are free text on keys.** §5.2 has a
  `projects(id, team_id, name)` table. Today a project budget needs at least
  one active key in that project, so you can't set a budget before the keys
  exist. Do we make projects real?
- **Decide: what should "throttle" do?** Requests over a throttle cap are
  admitted and marked; nothing slows down. Options: a per-key rate limit
  (Envoy's `BackendTrafficPolicy`) while over cap, or reject a fraction with
  429 and Retry-After.
- **Defaults I picked:**
  - One budget per scope, enforced by a unique index.
  - Monthly budgets only, since spend is month to date.
  - A key-scoped budget matches by key name.
- **Overshoot.** Warden reads spend from its 5s snapshot of
  `receipts_daily`, so a key can overspend by up to about 5s of traffic plus
  ingest lag, plus whatever is in flight. Fine for monthly caps; not a hard
  real-time limit.

## 3. Rules

Done: create, then drafts, publish (versioned, immutable), mode (enforce,
monitor, disabled), fail mode, rollback, history, and delete for rules that
aren't live. First publish defaults to monitor mode. Publish's `dryRun`
reports the change and says replay isn't connected.

- **Console builder follows the engine (decided 2026-09-30).** In api mode the
  Guardrails builder offers one all-of list of conditions and one action, and
  says the rest isn't connected. The three decisions below are still open;
  answering "several actions" or "nested groups" means engine work first.
- **Decide: one action per rule.** The engine applies only `then[0]`, so
  writes allow exactly one action. §5.3 shows several (redact and reroute).
  Support several, with §5.3's ordering semantics?
- **Decide: reordering.** New rules go last. Rule order decides the outcome
  (first block wins), and there's no write to reorder. Should a reorder be
  its own audited, versioned change?
- **Decide: rules vs. policies.** §5.2 versions *policies* (groups of
  rules); this versions each rule. Per-rule matches what Guardrails shows.
  OK to keep?
- **Redaction "rehydrate on return" isn't implemented.** The seed rules'
  details say it, but there's no vault or response-path rehydration
  (§4.5 step 5). Either build it (Warden response path) or change the seed
  text. Until then the Guardrails page states something that doesn't
  happen.
- History before migration 004 wasn't kept: seeded rules have only their
  current version, with `publishedAt`/`publishedBy` null.
- Replay (§7.5.7) is still a new system; it's what publish's dry run should
  return.

## 4. Aliases

Done: `PUT`/`DELETE /aliases/{alias}` with audit rows and If-Match (or
`If-None-Match: *` to create), and
aliases are per tenant. Overlapping `*` patterns now resolve by longest
prefix, which closes the open item from last session.

- **Defaults I picked:**
  - A pattern that would capture catalog models other than its target is
    refused (`gpt-*` → `gpt-5-mini` would silently reroute `gpt-5.5`).
  - An *exact* alias with a catalog model's name is allowed, as a
    deliberate redirect.
- Alias conditions (§5.2 `conditions jsonb`, e.g. "input tokens < 64k")
  aren't built.

## 5. Keys and rotation

Done: receipts record which secret authenticated them (`secret_id`, the
first 12 hex of the secret's SHA-256, unchanged when a rotation promotes the
new secret). A rotating key reports requests since the rotation started on
the old secret, the new one, and unrecorded. Extend-overlap and
retire-old-secret-now are in.

- **Check:** storing a 12-hex hash prefix per receipt. It can't be reversed
  and only tells a key's own secrets apart; say if policy forbids it.
- Overlap can't end more than 7 days from now, matching the rotate
  dialog's maximum.
- Key writes (revoke, rotate, extend, finish) don't have an etag yet, so
  they don't take If-Match (§7).

## 6. Not built, by decision

- **Routes and backends stay read-only (decided 2026-09-30).** Route config
  in Postgres only feeds the dev gateway's candidates and Warden's reroute
  hints. Real routing is Agent Router's `aigw/config.yaml` (static
  `AIGatewayRoute`), and there's no reconciler (§4.4), so an "apply" would
  change nothing on the real path. `GET /backends` and `GET /routes` report
  every sync state as `not_reconciled` instead of the seeded ones, and in
  api mode the Routing page has no edit, apply, adopt or YAML paths. It
  doesn't show the mockup's backend specs, reconcile events or failover
  log either; it links to fallback receipts instead. The reconciler
  (generate and apply `AIGatewayRoute`, or regenerate `config.yaml`
  locally) is its own project.
  - Still seed values in api mode: provenance (Console/Git/Adopted, and
    Git source links), backend health, and p50/errors for a backend that
    served fewer than 5 requests in the last hour. The page says health is
    as configured; provenance isn't labelled yet.
- **Detector thresholds are on hold (decided 2026-09-30).** The detectors
  are regexes (`gateway/detect.go`) with no confidence score, so a
  threshold would change nothing. They wait for real detectors
  (Presidio-style NER, per §5.3). The `detectors` table's threshold,
  `hits_24h` and `fp` are seed values; hit and false-positive counts from
  receipts are a §3 item.

## 7. Cross-cutting

- **Decide: auth and roles.** Every write acts as `dev@localhost`, and
  §5.2's roles aren't enforced. Before this leaves dev: who may publish
  rules, change prices, delete budgets?
- **If-Match is required (decided 2026-09-30).** Updating or deleting an
  alias, budget or rule without `If-Match` is a 428; a stale one is a 409
  with the current resource. Creating an alias with `PUT` takes
  `If-None-Match: *`. Dry runs don't need it. Key writes (revoke, rotate,
  extend, finish), price writes and the kill switch don't carry an etag yet,
  so they don't require it.
- **No database-backed Go tests.** SQL only runs in the api-mode suite
  against the live stack. That's where the ambiguous-column bug in the rule
  drafts query surfaced. I recommend a Postgres + Timescale test harness
  (testcontainers or the compose DBs) for the store package.
- **The api-mode suite writes to the live dev database.** It creates keys
  (revoked afterwards), budgets, rules and a scheduled price (all removed
  afterwards). Revoked test keys accumulate in the Keys list.
- Config writes reach Warden at once: the control plane calls Warden's
  `POST /reload` before responding. If Warden is unreachable, the write
  still succeeds and Warden catches up on its next 5s tick.
- Console work these endpoints unlock: forms for aliases and prices, still
  disabled in api mode. Budgets, rules and rotation are connected. (Sharing one SSE
  stream per tab is done.)
- Restarting after a server change: `make restart` (see server/README.md).
