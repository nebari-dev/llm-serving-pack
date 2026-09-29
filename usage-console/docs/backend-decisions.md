# Backend: decisions and open questions

Written 2026-09-29 at the end of the backend writes pass (`7659a28`..`dabb753`
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

- **Decide: `api_keys.budget_id` is now vestigial.** Enforcement goes by
  scope. Options: drop the column and the key form's budget field, or make
  the key form's "budget" create a key-scoped budget. I recommend dropping
  it; budgets are managed on Spend.
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
  - Deleting a budget clears `budget_id` on keys that named it.
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

Done: `PUT`/`DELETE /aliases/{alias}` with audit rows and If-Match, and
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
- Key writes (create, revoke, rotate) don't use If-Match yet.

## 6. Not built: needs a decision first

- **Routes and backends "apply".** Route config in Postgres only feeds the
  dev gateway's candidates and Warden's reroute hints. Real routing is
  Agent Router's `aigw/config.yaml` (static `AIGatewayRoute`), and there's
  no reconciler (§4.4). An "apply" would change nothing on the real path,
  or claim it had. The seeded sync states ("drift", "applying") and backend
  health/p50 are fixtures that api mode shows as real; the Routing page
  isn't in the real-data checklist yet.
  - **Decide:**
    - (a) Build the reconciler: generate `AIGatewayRoute` and SSA it in a
      cluster, or regenerate `config.yaml` and restart aigw locally.
    - (b) Writes store the desired state, and a sync state of "not applied"
      says so.
    - (c) Keep routing read-only, and mark the fixture states as not
      connected.
  - I recommend (c) now and (a) as its own project.
- **Detector thresholds.** The detectors are regexes (`gateway/detect.go`),
  with no confidence score, so a threshold changes nothing. The
  `detectors` table's threshold, `hits_24h` and `fp` are seed values.
  - **Decide:** real detectors first (Presidio-style NER, per §5.3), or
    show thresholds read-only as "not used by the regex detectors" until
    then. Hit and false-positive counts should come from receipts (a §3
    item).

## 7. Cross-cutting

- **Decide: auth and roles.** Every write acts as `dev@localhost`, and
  §5.2's roles aren't enforced. Before this leaves dev: who may publish
  rules, change prices, delete budgets?
- **Decide: require If-Match?** §6 wants optimistic concurrency on every
  mutable resource. Writes accept `If-Match` and return 409 with the
  current row when it's stale, but a write without `If-Match` goes through.
  Once the console sends it, should a missing one be a 428?
- **No database-backed Go tests.** SQL only runs in the api-mode suite
  against the live stack. That's where the ambiguous-column bug in the rule
  drafts query surfaced. I recommend a Postgres + Timescale test harness
  (testcontainers or the compose DBs) for the store package.
- **The api-mode suite writes to the live dev database.** It creates keys
  (revoked afterwards), budgets, rules and a scheduled price (all removed
  afterwards). Revoked test keys accumulate in the Keys list.
- Warden picks up config writes on its next 5s reload. A `POST /reload` on
  Warden's admin port, called after writes, would make enforcement
  immediate.
- Console work these endpoints unlock: forms for budgets, rules, aliases,
  prices and rotation. All still disabled in api mode, plus sharing one SSE
  stream per tab. The mock-mode budget trace text still uses the old
  budget_id wording.
