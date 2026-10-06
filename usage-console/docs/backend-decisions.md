# Backend: decisions and open questions

Written 2026-09-29 at the end of the backend writes pass (`7659a28` onward
on `backend-api`). Each item says what the code does today, what's open,
and a recommendation. Items marked **Decide** block work; the rest are
defaults I picked that you may want to change.

## 1. Pricing: where do prices come from?

**Today.** `model_pricing` holds effective-dated rows per model: input,
cached input, output, reasoning, per 1M tokens. The only rows are the demo
seed (`internal/demo`, effective 2026-01-01). receipt-ingest costs each
settled receipt at the row in effect at its request's start time (rows read
per OTLP batch; 2026-10-05, spec §5.1), and stores that row on the receipt
as `cost_basis`, so later price changes never reprice history. New in this pass:
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

**Decided (2026-10-05): LiteLLM by default, manual overrides per backend.**
- Prices are per (model, backend). A small mapping table links each pair
  to its LiteLLM key (for example `claude-sonnet-5` via `bedrock-eu` maps
  to a `bedrock/...` key).
- A daily job reads LiteLLM's file. A change to a rate nobody overrode
  applies automatically, as a new effective-dated row with an audit row.
- Manual overrides can be partial: override one rate (say input) and the
  others keep following LiteLLM.
- When LiteLLM changes a rate that has a manual override, the job doesn't
  apply it. It proposes it ("LiteLLM's price moved since you set this
  override"), and someone accepts or dismisses it.
- A pair with no LiteLLM entry and no override (self-hosted
  `vllm-internal`) shows "no price", not $0, until someone sets a rate.
- Each receipt's `cost_basis` records which source priced each rate.
- Read "per rate": a LiteLLM change to a rate nobody overrode still
  applies automatically, even if another rate on the same pair is
  overridden.
- Decided the same day:
  - A receipt for a pair with no price stores no cost. When someone
    later sets a rate for that pair, those receipts are priced at it.
  - The seed rows are retired at the first sync. Pairs LiteLLM covers
    move to its rates; a pair it doesn't cover (`vllm-internal`) has no
    price after that.
  - Cache writes are in scope. That means a cache-write rate and a
    cache-write token count on receipts.
  - Long-context, batch and priority tiers are still open.
- Built 2026-10-05. See `docs/console-real-data.md` (Model prices, Pricing
  sync) for what shipped. Defaults I picked:
  - Price writes require If-Match. The sync writes the same rows, so a
    stale edit would overwrite it.
  - Accepting a proposal puts the rate back on LiteLLM. A manual edit to a
    rate dismisses its open proposal.
  - A sync change starts at the moment of the sync. If a manual change is
    scheduled, the synced row runs until it.
  - LiteLLM has no cached, cache-write or reasoning rate for some entries.
    Those tokens then bill at its input or output rate.
- **Decided and built 2026-10-06: reasoning bills once, inside output.**
  The cost formula used to add `reasoning_tokens × reasoning rate` to
  `output_tokens × output rate`, but OpenAI (and so Agent Router) counts
  reasoning inside `completion_tokens`, so real traffic was charged twice.
  Now it's the recommendation below; the evidence follows.
  - **Confirmed (2026-10-05), from Agent Router's source**
    (`theagentrouter/agent-router` tag `v1.1.0`, commit `c217da8a`):
    - Our generated config (see
      `server/internal/routing/testdata/config-2026-10-05.yaml:253-256`), as
      `aigw run`'s `internal/autoconfig/config.yaml.tmpl:220-225`, maps
      `llm_output_token` to cost type `OutputToken` and
      `llm_reasoning_token` to `ReasoningToken`. Our access log
      (`server/aigw/base.yaml:111-112`) logs them as
      `gen_ai.usage.output_tokens` and `gen_ai.usage.reasoning_tokens`.
    - `internal/extproc/processor_impl.go:850-871` (`evalCost`) copies each
      counter as is: `OutputToken` is `costs.OutputTokens()`, `ReasoningToken`
      is `costs.ReasoningTokens()`. Nothing is subtracted. They're written once,
      at end of stream, on success (`processor_impl.go:638-647`). A missing
      count is written as 0.
    - OpenAI schema (`internal/translator/openai_openai.go:165-174`,
      streaming `207-218`): `SetOutputTokens(completion_tokens)` and
      `SetReasoningTokens(completion_tokens_details.reasoning_tokens)`. So
      for usage `{completion_tokens: C, reasoning_tokens: R}`,
      `llm_output_token` = **C** (reasoning included) and
      `llm_reasoning_token` = **R**, a part of C.
    - Anthropic, native `/v1/messages` (`anthropic_anthropic.go:118-124`
      via `internal/metrics/metrics.go:292-307`): output = `output_tokens`,
      which includes thinking; reasoning is never set, so 0. Input is
      `input_tokens` + cache reads + cache writes. An OpenAI-schema request
      to Bedrock or Vertex Anthropic (`anthropic_helper.go:1303`, streaming
      `1181-1182`) sets reasoning from `output_tokens_details.thinking_tokens`
      when the upstream sends it (inside output again), else 0.
    - An Anthropic-style caller (Messages API, §6) translated to an
      OpenAI-schema backend (`anthropic_openai.go:124-129`, streaming
      `openai_helper.go:737-750`): input = `prompt_tokens`, output =
      `completion_tokens`, cached input and reasoning never set, so 0. Its
      cached input bills at the input rate and its reasoning at the output
      rate, inside output.
    - So today an OpenAI reasoning response bills R twice. Anthropic doesn't,
      but its thinking never sees a reasoning rate (it bills at output, which
      is what Anthropic charges). `receipts.total_tokens` (input + output +
      reasoning, `store/receipts.go`) and the drawer's cost lines count R
      twice too.
  - The fake upstream now takes `X-Fake-Reasoning: n` to report n reasoning
    tokens OpenAI's way (`fakellm.OpenAIReasoning`). An api-mode test sends
    one and asserts `outputTokens` = `completion_tokens` and
    `reasoningTokens` = n, the receipt's cost, and the drawer's lines.
  - **Built (user's go-ahead 2026-10-06).** Treat reasoning as a part of output, as cached
    input is a part of input: bill `max(output − reasoning, 0) × output rate
    + reasoning × reasoning rate`. When the reasoning rate equals the output
    rate (LiteLLM's fallback) that's `output × output rate`. Anthropic's
    reasoning is 0, so its cost doesn't change. Change with it:
    `total_tokens` = input + output, the drawer's output line shows output
    − reasoning, and `fakellm.Simulate` reports OpenAI's way (or its
    traffic under-bills). Receipts already written keep their cost; an
    unpriced one priced later uses the new formula.

**Decide: schema gaps a real price list exposes.** Per-backend prices and
cache writes are done (above).
- **Long-context tiers** (LiteLLM's `…_above_200k_tokens`), plus batch and
  priority tiers. There's one rate per token type today.
- **Who may change prices.** The catalog is shared by every tenant, so a
  change reprices everyone. There are no roles yet (see §7); it should be a
  platform-admin action.

Fixed 2026-10-05: ingest prices at the receipt's start time (spec §5.1),
from the rows in the db per OTLP batch, not the snapshot loaded up to 5s
earlier. Defaults I picked:
- A receipt that arrives unpriced is priced later at the row in effect at
  its own time if that prices it (ingest hadn't seen the row yet; not
  marked `pricedLater`), else at the first row set after it that does, once
  that row is in effect.
- Unpriced requests are counted from raw receipts, not new aggregate
  columns: rebuilding `receipts_5m`/`receipts_daily` would lose history
  past raw retention (30 days). So unpriced counts reach back 30 days; a
  month-to-date count on the 31st can miss the first day's.
- Spend CSV writes `spend_usd` as "no price" for a row with nothing priced,
  and adds `unpriced_requests`. Budgets don't count unpriced requests
  against the cap (they have no cost); the budget shows how many.

**Built 2026-10-06: savings analysis (spec §7.5.5).** `GET /spend/savings`;
method in `internal/api/savings.go` and on the page. It reads raw receipts
(30 days) rather than new aggregate columns, for the reason above. Defaults
I picked, all open to change:
- "Would plausibly have served" = the answer was at most **1,000 output
  tokens** (reasoning included) and input + output fits the cheaper model's
  context. One fixed number, not a setting. Quality isn't measured; the page
  says to try the cheaper model on part of the traffic first.
- "Same family" is `model_catalog.family`, which is coarse: Opus 4.1 →
  Haiku 4.5 counts (both `claude`).
- The cheaper model bills the same token counts, cache hits included
  (switching models would usually lose them), on the cheapest backend that
  offers it, not the one routing would pick.
- The headline is what the last 30 days would have saved (or the days of
  receipts there are), not extrapolated to a month.
- A key that doesn't allow the cheaper model still counts; the page names it,
  since moving it first needs the model added.

**Built 2026-10-06: close report PDF (§7.5.5).** `GET
/spend/close-report?month=YYYY-MM`. Defaults I picked: budgets show caps as
configured at export time (cap history is only in the audit log); unpriced
counts are partial for a month that started over 30 days ago and "not
known" for one that ended before that; the audit row is kind `Export`, and
Overview's featured change and Activity's traffic effect skip that kind.

## 2. Budgets

Done: every budget whose scope covers a key is enforced (team, project,
key), and the strictest over-cap one decides. There's create/edit/delete
with dry runs, and a $0.01 cap set through the API stops real requests at
Agent Router (api-mode test).

- **Decided 2026-09-30: `api_keys.budget_id` is dropped** (migration 005).
  Keys don't name a budget; the key form lists the budgets that will cover
  the new key, and budgets are managed on Spend.
- **Decided 2026-10-05: projects become a table (built, migration 011).**
  §5.2's `projects(id, team_id, name)`, so a project budget can be set up
  before any key in it exists. Keys reference one of their team's projects;
  a project budget names it by id.
- **Decided 2026-10-05: projects go by id everywhere, and names are for
  people (built, config migrations 014–015, receipts 008).** Receipts carry
  `project_id`; Spend groups, Traffic filters and "project is" rules match
  by it, so two teams' "helpdesk" stay apart and a rename changes nothing
  but the label. Names are any text (trimmed, 1–80 characters, no control
  characters), unique per team ignoring case. `POST /keys` takes
  `projectId` (or a name the team has) and refuses an unknown one; it no
  longer creates projects. `PUT /projects/{id}` renames (If-Match, audited);
  `DELETE` is refused with the reason while the project has an active key
  or a budget, and otherwise marks it deleted, so revoked keys' history
  keeps a name. Defaults I picked: the aggregates aren't rebuilt (they keep
  `key_id` and a key never changes project, so Spend maps keys to projects;
  a rebuild would lose aggregate history older than raw receipts' 30 days);
  older receipts are backfilled from their key's project, exactly, at the
  next start of stargate-api; receipts keep the name they were made under.
- **Decided 2026-10-05: "throttle" is a per-key rate while over the cap
  (built; replaces the share-refused version, spec §11 Phase 4).** While
  any throttle budget covering a key is over its cap, the key gets 10
  requests in any minute (sliding window). The next gets 429
  `budget_throttled` with `Retry-After` = seconds until its oldest request
  leaves the window. Ten a minute keeps a person-driven app usable while
  the batch job or runaway agent that overspent crawls; it's one number
  for every budget (budgets carry it as `throttlePerMinute`), not a
  setting, to keep the form simple. Receipts say `throttled` (own verdict
  and trace state, degraded hue), not `blocked`, so Overview, Activity
  and Traffic count it apart; it's still a 429 in error rates. Counters
  are in Warden's memory: right for one Warden, but each replica would
  allow the rate (a shared counter, e.g. Envoy's rate-limit service, is
  the fix then), and a restart forgets them.
- **Decided 2026-10-05: a key-scoped budget matches by key ID (built,
  migration 010).** It matched by key name, which a rename or a reused name
  would break. The API adds `scopeName` for display.
- **Defaults I picked:**
  - One budget per scope, enforced by a unique index.
  - Monthly budgets only, since spend is month to date.
- **Overshoot.** Warden reads spend from its 5s snapshot of
  `receipts_daily`, so a key can overspend by up to about 5s of traffic plus
  ingest lag, plus whatever is in flight. Fine for monthly caps; not a hard
  real-time limit.

## 3. Rules and policies

Done: policies as §5.2 has them (built 2026-10-06). A policy is an ordered
list of rules with one mode (draft, enforce, monitor, disabled) and one fail
mode, and it's what is created, drafted, published (versioned, immutable),
rolled back, reordered and deleted (when not live). First publish defaults to
monitor mode. Publish's `dryRun` reports the change, its reroute conflicts,
and its replay (built 2026-10-06, below).

- **Console builder follows the engine (decided 2026-09-30).** In api mode the
  Guardrails builder offers, per rule, one all-of list of conditions, and says
  groups and "any of" aren't connected.
- **Policies (decided 2026-10-05, built 2026-10-06).** Config migration 045
  adds `policies`, `policy_versions` (a version holds the rules, in order)
  and `policy_drafts`; `policy_rules` now holds each policy's live rules
  (`policy_id`, `ordinal`, `name`, `when`, `then`). `policy_rule_versions`
  and `policy_rule_drafts` are gone. API: `GET/POST /policies`,
  `PUT /policies/order`, `PUT`/`DELETE /policies/{id}/draft`,
  `POST /policies/{id}/publish` (`?dryRun=true`), `/rollback`,
  `GET /policies/{id}/versions`, `DELETE /policies/{id}`; `/rules/*` is gone
  except `GET /rules/vocabulary`. Audit rows say "Created policy", "Published
  policy", "Reordered policies" and so on, target kind `Policy` as before.
  A rule keeps its id across versions (new rules get one on save), and a
  receipt's rule evaluation now names its policy (`policyId`, `policy`,
  `version` is the policy's).
- **Decide: how existing rules were grouped (chose: one policy per rule).**
  Each rule became a policy of its own with the same id, name, description,
  mode, fail mode, version and place in the order, holding the rule under
  the same id and name. It's the only grouping that changes no decision:
  rules had their own modes and fail modes (card-numbers is monitor,
  cost-guard-opus fails open), which a shared policy would have to merge.
  Each rule's version history became its policy's, version for version, so
  rollback still reaches every old version; old receipts' `ruleId` and old
  audit rows' target id are the policy's id, so counts and Activity still
  line up. A database seeded after migration 004 had no version rows for its
  seeded rules; 045 records their live version (publishedAt null), as 004
  did. `gateway.TestMigratedPoliciesDecideAsTheRulesDid` replays 302
  requests recorded from the engine before policies (seeded rules plus
  monitor, disabled, draft, a second reroute, deadlines) and gets the same
  decisions. Merging them into fewer policies is left to authors.
- **Ordering (§5.3, built 2026-10-06).** Policies evaluate in order, and a
  policy's rules in theirs, as one sequence: the first block wins and stops
  everything after it, redactions accumulate, the last reroute wins.
  Across policies is our extension of §5.3, which only speaks of rules within
  a policy; it's what makes one-policy-per-rule behave as before. A
  monitoring policy records "would …" for each action. Past the evaluation
  deadline a rule takes its policy's fail mode; Warden's own fail mode (no
  answer, panic) fails closed if any enforcing policy does. Traces and errors
  name a rule `policy/rule vN`, or just `rule vN` when the policy has its
  name ("Rule no-web v1 blocks this request." reads as before).
- **More than one action per rule (decided 2026-10-05, built 2026-10-06).**
  A rule may take several actions, at most one of each kind: redact and
  route to together, say. Block stands alone: with others it's refused,
  saying "block wins: a blocked request is refused, so redact and route to
  would never run". The engine still lets a block win if one gets through.
- **Reroute conflicts are warnings, at authoring time.** Rules in a policy
  that both reroute, and enforcing policies before or after it that reroute
  too, are listed on the policy (`warnings`) and in publish's dry run. Not
  refused: last-write-wins is the rule.
- **Decide: fail mode per policy, not per rule.** §4.5 and §5.2 put it on the
  policy; §5.3's example shows `fail_mode` on a rule. We follow §5.2.
- **Decided 2026-10-05: build rehydration (built).** A vault for the
  values Warden redacts, and the response-side swap in Warden that puts them
  back (§4.5 step 5). The vault is in memory on the request's ext_proc
  stream, so no table and no TTL beyond the stream. Warden now sees the
  response body (streamed), and skips it when there's nothing to restore.
  See console-real-data.md.
- **Decide: what rehydrates.** Per rule, as §5.3's `"rehydrate": true`: only
  a redact action whose detail says "rehydrate on return". "No rehydrate",
  or saying nothing, keeps placeholders in the response; the builder's
  switch starts off. Values are restored only in the reply's choices
  (message and delta text, tool-call arguments), not in headers. A compressed
  reply isn't restored, and the receipt says so. Confirm these defaults.
- History before migration 004 wasn't kept: seeded policies have only their
  current version, with `publishedAt`/`publishedBy` null.
- **Replay and per-route capture (built 2026-10-06, user's spec: capture
  is opt-in per route; replay content exactly where a route captured, and
  metadata-only rules over everything else; say which each result is).**
  What shipped is in console-real-data.md (Replay). Defaults I picked:
  - **What's stored is masked.** §4.6 says "raw prompt/response capture",
    §9.2 says detected values never appear in what's kept. I followed §9.2:
    every detector match is a placeholder, whether or not a rule acted on it.
    Text no detector recognises (names, addresses) is kept as sent, and the
    confirm dialog says so. Replay stays exact for "contains entity" because
    each placeholder turns back into a made-up value of its entity. Catch: a
    prompt that literally contains `[EMAIL_1]` replays as an email.
  - **Which route.** The one the request takes as the caller sent it (model
    and headers), among the routes the gateway runs. A request a policy
    reroutes still counts as its original route's.
  - **Capture applies at once**, not on the next routing apply: it changes
    Warden, not the gateway's config. Its role is "capture" (security,
    admin), like reveal.
  - **The response kept is what the caller got** (rehydrated, then masked),
    up to 1 MiB.
  - **Replay ignores budgets, allowlists and backend health** (every backend
    healthy), so a result is the policies' doing; it reads requests that
    reached policy evaluation (not those refused by the key check, a budget
    or the model), at most the newest 10,000.
  - **The draft is replayed as enforced**, in the place of its policy (a new
    one last), against the others as they are, monitoring ones included. The
    publish dialog says monitor mode records rather than acts.
  - **Replay is a read** (every role): it writes nothing and shows no
    content, only counts and receipt links. No audit row.
  - **Windows** 1h (default, as the spec's mockup), 24h, 7d, 30d.
  - Receipts from before 2026-10-06 have no recorded `x-data-region`, so a
    region rule treats them as having none.
- **Custom entities (built 2026-10-06).** §5.3's registry, as regexes: a
  name, a pattern, a placeholder label, and examples that must and mustn't
  match, checked on every save. Warden loads them in its snapshot; rules
  name them like built-ins. Patterns are Go RE2 (linear time, so nothing
  catastrophic), at most 512 characters and 1,000 compiled instructions, and
  never match empty text. Decide:
  - **Names can't change**, since rules name an entity by its name; make a
    new one instead. Alternative: rename and rewrite the rules that name it.
  - **Delete is refused while any rule names it** (live, disabled or a
    draft), since the rule would silently stop matching. Old versions don't
    count: rolling back to one that names a deleted entity republishes a
    rule that never matches. Rollback doesn't re-validate today.
  - **Examples are stored** with the entity, so the next editor sees them.
    The form says to use made-up values, but nothing stops a real one.
  - **Labels can't repeat another detector's**, so a placeholder names one
    entity. Placeholders would stay unique without this.
  - Only regexes. NER, entropy and validators like Luhn (§5.3) wait for real
    detectors, with thresholds (§6).
- **False-positive review (built 2026-10-06).** A reviewer marks a hit (an
  entity a receipt recorded, redacted or blocked) "false positive" or
  "correct"; Detectors counts both over 30 days. Verdicts live in the config
  database, beside the audit log, so each one and its audit row commit
  together. Decide:
  - **What a reviewer sees.** Receipts keep hashes; the matched value is
    never stored anywhere. The queue shows metadata only (entity, count,
    action, rule, key, team, model, time), and links to the receipt, where
    captured content (prompt as sent, placeholders in place of matches) can
    be revealed with an audit row. The queue doesn't show content inline,
    so reading it always goes through the audited reveal. Most receipts,
    and every block, have no content; a verdict on those is a judgement on
    metadata, and the page says so.
  - **Granularity is per entity per request**, not per match: a request
    with two email matches is one hit. Receipts don't record matches
    individually.
  - **Monitor-mode matches aren't reviewable**: the receipt records "would
    redact" with no entity. Recording the entity there would make them so.
  - **Window**: the queue reaches back 7 days (200 receipts with hits,
    newest first); counts use 30 days, the raw-receipt window.
  - A verdict can be changed, with If-Match on the review state, and the
    audit row keeps the earlier one. There's no "un-review".
  - Verdicts don't change detection yet. They're the data a threshold or a
    pattern edit would be tuned on.

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

- **Routes and backends: desired state, applied locally (2026-10-05).** This
  replaces the read-only decision of 2026-09-30. Routes and backends in
  Postgres are the desired state, and `internal/routing` compiles them to
  the gateway's resources. Edits save at once, with an audit row and
  If-Match. The gateway changes only on an explicit apply: one review of the
  CRD diff against what it runs, then one restart (user's choice: save, then
  apply all). The local applier writes `tmp/aigw/config.yaml`, which is
  `aigw/base.yaml` (infrastructure, checked in) plus the compiled routing,
  and restarts aigw, rolling back on failure (user's choice: split base and
  generated rather than rewrite the checked-in file).
  - Held until the llm-serving-pack ownership questions are answered: drift,
    adopt, provenance (the seeded Console/Git/Adopted values are no longer
    shown in api mode), and a Kubernetes applier. The survey's direction is
    that Stargate only writes AIGatewayRoutes it owns.
  - Per-route content capture is built (2026-10-06, §3 Replay): Warden
    decides it from the running routes; nothing about it compiles into the
    gateway's config.
  - **Providers and their keys (decided 2026-10-05, built).** Backends are
    created, edited and deleted like routes (audit rows, If-Match; delete is
    refused while a route sends to one). Providers are API-key ones only:
    OpenAI, Anthropic, any OpenAI-compatible endpoint, and self-hosted with
    no key, each a base URL plus an optional key. Bedrock, Azure and Vertex
    are shown disabled: they need cloud credentials.
    - Keys (user's choice): Postgres keeps the reference (an env var name,
      `STARGATE_PROVIDER_KEY_<NAME>`), the key's prefix (at most 8 characters
      and a third of the key), when it was set, and the last connection
      test. The key goes to a `routing.KeyStore`; the local one stages it in
      the owner-only `tmp/aigw/provider-keys.pending.env` (test stack:
      `tmp/aigw-test/…`), and the apply promotes that into
      `provider-keys.env`, which aigw is started with. A Kubernetes one
      would write the Secret instead. No response carries a key.
    - A replaced key is a pending change: the compiled Secret carries a
      `stargate.dev/key-version` annotation (when the key was set), so the
      plan shows `Secret/<name>-key: key replaced`, its etag moves, and the
      apply's restart loads the key. The test stack's apply now recreates
      the container (`test-stack.sh aigw`), since `docker restart` keeps the
      old environment.
    - A saved key takes effect only on apply (spec §7.1 principle 4, fixed
      2026-10-05): it waits in the staged file, which nothing starts aigw
      with, so a gateway restarted for any other reason still sends the
      applied key. The apply promotes the staged file with the config and,
      if the gateway doesn't come back, puts both back (the staged keys stay
      staged). Removing a backend's key is staged the same way.
    - A key that fails its test isn't saved (spec §7.5.1 "Tested once, then
      sealed", fixed 2026-10-05): creating a backend with a key, or
      replacing one, tests it first; on failure the answer is a 422
      `key_test_failed` with the provider's error (key scrubbed), and
      nothing is stored, audited or made pending. A backend with no key is
      still saved, then tested (a self-hosted one may be down).
    - Defaults I picked, to confirm:
      - A provider's error is shown verbatim except for the key: the key
        itself, any 6+ characters of it past the prefix, and masked echoes
        like OpenAI's `sk-proj-****abcd` are taken out.
      - A model the catalog doesn't know is added to it (display = id,
        provider = the backend's, context 0 = unknown) with no price and no
        LiteLLM key: "no price" until someone sets a rate or a key on Models.
        No LiteLLM key is guessed, even for OpenAI.
      - Anthropic serves both kinds of caller from one provider (built
        2026-10-06, the user's ask: Anthropic-style callers with the same
        governance). Its connection test is native (`GET /v1/models` with
        `x-api-key` and `anthropic-version`). What aigw v1.1.0 does,
        checked in its source (`github.com/envoyproxy/ai-gateway@v1.1.0`)
        and against a probe gateway:
        - It serves Anthropic's Messages API at `/anthropic/v1/messages`
          (and `/count_tokens`, `/anthropic/v1/models`;
          `cmd/extproc/mainlib/main.go:347-353`), from the same
          AIGatewayRoutes as `/v1/chat/completions`: every generated
          HTTPRoute rule matches path prefix `/` plus the rule's headers
          (`internal/controller/ai_gateway_route.go:316-320`), and an
          AIGatewayRoute rule can match headers only.
        - The backend's schema then decides (`endpointspec.go:400-417`):
          Messages to `schema: Anthropic` passes through to
          `{prefix}/messages`; Messages to `schema: OpenAI` is translated
          to chat completions and back (also AWSBedrock, GCP/AWS
          Anthropic). Chat completions to `schema: Anthropic` fails: the
          probe got a 500, "unsupported API schema" (`endpointspec.go:164-180`
          has no OpenAI→Anthropic translator for a direct backend, only
          `GCPAnthropic`/`AWSAnthropic`; ai-gateway's main branch as of
          2026-10-05 neither). One rule can list both kinds of backend; on
          a mismatch the request fails rather than skipping the backend.
          Priority failover from a native to a translated backend worked
          in the probe once passive health checking ejected the first.
        - Dynamic metadata: `backend_name` is the AIServiceBackend's
          route-scoped name; native Messages log input = `input_tokens` +
          cache reads + writes, cached, cache writes and output (thinking
          inside, reasoning 0), streamed too (message_start and
          message_delta usage). Translated Messages log only input and
          output (`anthropic_openai.go:124-129`; streamed, from the usage
          chunk with no choices, `openai_helper.go:737-750`): cached input
          and reasoning are 0, so they bill at the input and output rates.
          `x-ai-eg-model` is set for Messages as for chat.
        - Envoy Gateway's HTTP ext_authz gets only `Authorization` and the
          request line unless `headersToExtAuth` names more: the probe got
          401s for `x-api-key` until it did.
        - aigw adds a route-not-found rule to each AIGatewayRoute's
          HTTPRoute, so one with 16 rules is refused ("spec.rules: Too
          many: 17"). The old split at 16 (`routing.MaxRules`) never hit it
          with 9 rules in use; the doubled seed did. It's 14 now, keeping
          pairs together.

        What's built: an Anthropic backend compiles to `<name>` (schema
        OpenAI, `APIKey`, for OpenAI-style callers, as before) and
        `<name>-native` (schema Anthropic, the same prefix, Backend and
        Secret, and a `BackendSecurityPolicy` `<name>-native-key` of type
        `AnthropicAPIKey`). The key check takes the gateway key from
        `Authorization` or `x-api-key`, answers `/anthropic/...` callers in
        Anthropic's error shape (with our `code` alongside), and marks them
        `x-stargate-api: anthropic` (the ClientTrafficPolicy strips any a
        caller sends). Each rule and reroute hint gets a copy right after
        it that also matches that header and sends to the `-native` twins:
        one more header match always outranks the originals for
        Anthropic-style callers and keeps their order among the copies, and
        OpenAI-style callers never match one, so their routing is
        unchanged. A rule and its copy share an AIGatewayRoute (7 routes
        each), so a route edit is one change; with no Anthropic backend
        there are no copies. Warden reads Messages bodies (system prompt,
        text blocks, tool results; images and the model's own tool calls go
        as sent), rewrites them in place, refuses in Anthropic's error
        shape, and rehydrates Messages responses (content blocks; in the
        event stream, text, partial_json and thinking deltas, flushing held
        text before content_block_stop). Receipts map `<name>-native` back
        to `<name>`, priced as it, and the route step says "Anthropic
        Messages API".
        Defaults I picked, to confirm: the native twin is automatic for
        provider Anthropic (no toggle); `-native` names are reserved for
        new backends and an Anthropic backend's name is at most 52
        characters (its policy is `<name>-native-key`); the seeded
        `anthropic-prod` (a fake) gets a twin too, which fake-openai now
        serves.
        Still open: OpenAI-style callers can't reach Anthropic's native API
        until upstream adds the translator, so they keep the
        OpenAI-compatible endpoint (no prompt caching or thinking blocks
        there).
      - `localhost`/`127.0.0.1` in a base URL compiles to
        `${STARGATE_HOST:-…}`, like the seeded fake backends, so the Docker
        test gateway can reach the host.
      - Deleting a backend stages its key's removal (gone from the gateway's
        file on the next apply, with the backend) and keeps its prices as
        history. A backend name is unique across tenants
        (the existing primary key).
    - Onboarding's "Connect a new provider" (spec §7.3, fixed 2026-10-05)
      saves the provider, then a route for its models ("Route … to …"),
      then lists every pending routing change and says it applies them all
      ("Apply all N changes", with the listed plan's etag, so nothing
      unseen is applied). Applying only that provider's changes was
      considered and not done: the applier applies one whole config, and a
      partial one would have to splice this provider's rules into the
      shared AIGatewayRoutes (ordered, split at 14 rules) while leaving
      other pending edits out, a config nobody reviewed as a whole.
  - Rule order: the gateway tries rules with more header matches first;
    among equals, in rule order. A new route goes ahead of any catch-all
    (`*` with no headers), and two routes can't claim the same model with
    the same headers.
- **Detector thresholds are on hold (decided 2026-09-30).** The detectors
  are regexes (`gateway/detect.go`) with no confidence score, so a
  threshold would change nothing. They wait for real detectors
  (Presidio-style NER, per §5.3). The `detectors` table's threshold,
  `hits_24h` and `fp` are seed values nothing reads; hit counts come from
  receipts, and false-positive counts from reviewers' verdicts (§3).

## 7. Cross-cutting

- **Auth and roles (decided 2026-10-05, built 2026-10-06).** Writes act as
  the signed-in user and every write checks the table below. Dev mode (no
  `-oidc-issuer`) is unchanged: everyone is `dev@localhost`, owner.
  - Roles are the spec's (§5.2): `owner | admin | editor | viewer | finance |
    security`. They come from Keycloak groups in the OIDC token (the `groups`
    claim, as llm-serving-pack uses in realm `nebari`), not from roles
    assigned in Stargate. `users.role` is a cache of the last token, not the
    source.
  - Dev mode keeps `dev@localhost` as `owner`.
  - Who may do what:

    | Action | Roles |
    |---|---|
    | Read everything | viewer and up (every role) |
    | Draft policies (create, edit or discard a draft) | editor, security |
    | Publish, roll back, reorder or delete policies; kill switch; capture | security, admin |
    | Change prices; accept or dismiss price proposals | admin (user, 2026-10-05: prices are shared by every tenant, so a change reprices everyone; restrict to admin when roles are built, not before) |
    | Create, edit or delete budgets | finance, admin |
    | Aliases and routing | editor, admin |
    | Own keys: create, revoke, rotate, extend, finish | the key's owner |
    | Anyone else's keys | admin |
    | Assign roles (Keycloak group mapping) | owner |

    `owner` can do everything. A denied write is a 403 naming the roles
    that may do it (spec §7.6 "Permission denied"), and the console shows
    those controls disabled with that reason.
  - What's built (2026-10-06):
    - Sign-in is a backend-for-frontend: the control plane runs the
      authorization code flow with PKCE as a confidential client, validates
      the ID token (JWKS signature, issuer, audience, expiry, nonce) and
      keeps it in an HttpOnly, SameSite=Lax cookie, renewed with the refresh
      token. Chosen over PKCE in the browser because the console's SSE
      stream is an `EventSource`, which can't send an `Authorization`
      header (the token would go in the URL, into logs), and downloads
      (exports, the close report PDF) would need the same workaround; and
      because no token is then readable by script. Vite proxies `/api`, so
      the cookie is same-origin with no CORS. Bearer access tokens are
      accepted too, for scripts. Validation is our own code on the standard
      library (`internal/auth`, about 250 lines: RS256, PS256, ES256; `none`
      and HMAC refused), not a dependency.
    - Groups map to roles by name: `stargate-<role>` (the
      `-oidc-group-prefix`), full path or not. Nebari's own groups (`admin`,
      `developer`) grant nothing, so a Nebari admin isn't a Stargate admin
      by accident.
    - `users` (migration 055) caches each member's email, name and roles
      from their last token, with first and last seen; Settings → Members
      lists it and links to Keycloak's groups page for changes.
    - Keys have an owner (`api_keys.owner`), whoever created them. Existing
      keys got the actor of their "Created key" audit row, else
      `dev@localhost`: every write before sign-in was made as
      `dev@localhost`, so that's accurate. Once people sign in nobody is
      `dev@localhost`, so those keys are managed by admins only. There's no
      transfer of ownership.
    - The table lives in `internal/api/authz.go` (`routeActions`); a test
      fails when a write route has no entry, and at run time one without is
      owner-only. `GET /session` returns `permissions` (per action: allowed,
      and which roles may), which the console disables controls from.
    - A local Keycloak (`make keycloak`, compose profile `auth`, :8180)
      imports realm `nebari` with the client, a group per role and a test
      user in each (server/README.md).
  - Defaults I picked, to confirm:
    - Writes the table doesn't name: projects (create, rename, delete) are
      editor, finance and admin; reordering and deleting rules are with
      publishing (security, admin), since both change what's enforced;
      providers, provider keys, connection tests and routing apply are
      "aliases and routing" (editor, admin); price sync now, sources and
      proposals are prices (admin).
    - Reveal content is "capture" (security, admin). Receipt exports, the
      close report and the onboarding gateway test (it uses the caller's own
      gateway key) are reads: every role.
    - Creating a key needs any role, viewer included, as "own keys" reads.
      Say if viewers shouldn't mint keys.
    - Someone signed in with no Stargate group reads nothing (403), but
      `/session` answers, so the console can say who to ask. The alternative
      is everyone in the realm being a viewer.
    - The session cookie is a browser-session cookie holding the ID token
      (about 1.2KB); Keycloak's SSO session (realm settings) bounds how long
      refresh works. A refresh failure clears the cookies and the console
      signs in again.
  - Not built: service accounts with scoped tokens for CI (spec §6; a
    Keycloak client-credentials token with a `stargate-*` group would work
    as Bearer today), an audit row for sign-in itself, and changing roles
    from Stargate (by decision: Keycloak is the source).
- **If-Match is required (decided 2026-09-30).** Updating or deleting an
  alias, budget or rule without `If-Match` is a 428; a stale one is a 409
  with the current resource. Creating an alias with `PUT` takes
  `If-None-Match: *`. Dry runs don't need it. Key writes (revoke, rotate,
  extend, finish) and the kill switch don't carry an etag. They are toggles
  that give the same result if repeated, so they don't need one (decided
  2026-10-05). Replacing a provider key follows them (no If-Match, my
  default); editing or deleting a backend takes If-Match like a route.
  Provider writes ("Aliases and routing" in the table above) are editor and
  admin (enforced since 2026-10-06).
  - Setting a price requires `If-Match` too (decided 2026-10-05, built with
    the pricing slice): two editors changing the same rate concurrently has
    happened.
- **PDFs are written by `internal/pdf`, our own (2026-10-06, my default).**
  About 400 lines: text and rules, standard Helvetica (not embedded),
  WinAnsi, uncompressed streams, so the text in a report can be searched
  and tests read it. The libraries I weighed: `go-pdf/fpdf` (the
  maintained gofpdf fork) is archived; `signintech/gopdf` needs a TTF file
  shipped for any text; `johnfercher/maroto` sits on fpdf. A report is
  tables of text, so a dependency buys little. The catch: characters
  outside WinAnsi (Latin-1 plus typographic marks) print as "?", so a
  project named in, say, Japanese would. Embedding a Unicode font (or
  moving to gopdf with one) fixes that if it matters.
- **Database-backed Go tests (decided 2026-10-05, not built).** SQL currently
  runs only in the api-mode suite against the live stack. That's where the
  ambiguous-column bug in the rule drafts query surfaced.
  - The store package gets a harness on testcontainers-go with the
    TimescaleDB image, running the real migrations.
  - Tests skip when Docker isn't available, so `go test ./...` still works
    without it. The harness runs in CI, and doesn't depend on the dev
    stack's compose databases.
- **The api-mode suite writes to the live dev database.** It creates keys
  (revoked afterwards), budgets, rules and a scheduled price (all removed
  afterwards). Revoked test keys accumulate in the Keys list.
  - Decided 2026-10-05; built the same day, with one change: the suite needs
    the whole request path (key check, Warden, Agent Router, ingest), not
    just a database, so it runs against a second stack rather than the
    testcontainers harness. `server/scripts/test-stack.sh` (`make
    test-stack`) creates `stargate_test` and `receipts_test` in the dev
    compose containers, migrates and backfills them, and runs stargate-api
    (:9080/:9082), Warden (:9083/:9084) and ingest (:9317) on them, with
    Agent Router in Docker on :2975 (two can't share a host: it binds fixed
    internal ports). `npm run test:api` targets it, and the suite refuses
    any control plane whose environment isn't `test`. The Keys list will
    not hide test keys; it keeps showing the real state of the database.
- Config writes reach Warden at once: the control plane calls Warden's
  `POST /reload` before responding. If Warden is unreachable, the write
  still succeeds and Warden catches up on its next 5s tick.
- Console work these endpoints unlock: forms for aliases and prices, still
  disabled in api mode. Budgets, rules and rotation are connected. (Sharing one SSE
  stream per tab is done.)
- Restarting after a server change: `make restart` (see server/README.md).
- **Traffic sampling (2026-10-06, my defaults).** The stream samples each
  connection above 40 matching requests a second, measured after its
  filters. 40/s is just under the 2,500 rows a minute the virtualized table
  was measured to hold at a p95 of 17ms a frame: below it, the console keeps
  up, so sampling would hide rows for nothing. Sampling ends below 30/s, so a
  rate near 40 doesn't flap. N is a round number, not the exact ratio.
  Requests are kept by a hash of their id. Change `sampleThreshold` in
  `internal/api/stream.go` if you'd rather sample earlier, for readability.
- **Receipt signing key (2026-10-06, my default; check).** One Ed25519 key
  per control plane, made on first start in an owner-only PKCS#8 file
  (`-signing-key`, default `tmp/receipt-signing.pem`, gitignored; refused at
  start if group or others can read it). Not the config database: anyone
  with read access to it, or a backup of it, could then sign exports, and
  provider keys already stay out of it for the same reason (§6). A file maps
  to a Kubernetes Secret mounted read-only. Open: two API replicas would
  each make their own key, so in Kubernetes the Secret must exist before
  they start; key rotation (publishing old public keys) isn't built.
  - Signatures are per export, over the exported bytes, not §5.1's
    per-receipt `receipt_signature` at write time. An export proves what the
    control plane handed out, not that the database wasn't changed earlier.
    Signing each receipt at ingest would prove that; say if you want it.
  - Exports and reveals are audited with target kind `Receipt` and left out
    of `GET /changes` by default: Activity would otherwise compute a traffic
    effect for an export, and the drawer would call it the config change
    before a request. Open: show them on a page (an Access filter on
    Activity, or Settings).
  - Roles (2026-10-06, my default, see Auth and roles above): reveal is
    "capture" (security, admin), as §9.2 asks an elevated role for content;
    export is a read (every role), since it hands out what the caller can
    already see, audited under their name.
