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
- **Decide: reasoning tokens may be billed twice.** The cost formula adds
  `reasoning_tokens × reasoning rate` to `output_tokens × output rate`. The
  fake upstream's simulated traffic reports reasoning apart from completion
  tokens, so that holds for it. OpenAI counts reasoning inside
  `completion_tokens`, so real traffic is charged twice.
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
    - So today an OpenAI reasoning response bills R twice. Anthropic doesn't,
      but its thinking never sees a reasoning rate (it bills at output, which
      is what Anthropic charges). `receipts.total_tokens` (input + output +
      reasoning, `store/receipts.go`) and the drawer's cost lines count R
      twice too.
  - The fake upstream now takes `X-Fake-Reasoning: n` to report n reasoning
    tokens OpenAI's way (`fakellm.OpenAIReasoning`). An api-mode test sends
    one and asserts `outputTokens` = `completion_tokens` and
    `reasoningTokens` = n. Not run yet.
  - **Recommendation.** Treat reasoning as a part of output, as cached
    input is a part of input: bill `max(output − reasoning, 0) × output rate
    + reasoning × reasoning rate`. When the reasoning rate equals the output
    rate (LiteLLM's fallback) that's `output × output rate`. Anthropic's
    reasoning is 0, so its cost doesn't change. Change with it:
    `total_tokens` = input + output, the drawer's output line shows output
    − reasoning, and `fakellm.Simulate` reports OpenAI's way (or its
    traffic under-bills). Receipts already written keep their cost.

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

## 3. Rules

Done: create, then drafts, publish (versioned, immutable), mode (enforce,
monitor, disabled), fail mode, rollback, history, and delete for rules that
aren't live. First publish defaults to monitor mode. Publish's `dryRun`
reports the change and says replay isn't connected.

- **Console builder follows the engine (decided 2026-09-30).** In api mode the
  Guardrails builder offers one all-of list of conditions and one action, and
  says the rest isn't connected. Several actions means engine work first.
- **Decided 2026-10-05: more than one action per rule, later.** The engine
  applies only `then[0]` and writes allow one action; §5.3 shows several
  (redact and reroute), with its ordering semantics. Not first in line.
- **Reordering (built 2026-10-05).** New rules go last; `PUT /rules/order`
  moves one, checked against the order the author saw, with an audit row.
- **Decided 2026-10-05: follow §5.2's policies (not built).** Versioning
  and matching move to policies (groups of rules) as §5.2 has them, instead
  of each rule versioned on its own.
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
- History before migration 004 wasn't kept: seeded rules have only their
  current version, with `publishedAt`/`publishedBy` null.
- Replay (§7.5.7), re-running past traffic against a new rule to see what
  it would have caught, doesn't exist yet. It's what publish's dry run should
  return; until it does, the dry run can't tell you much.

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
  - Not built: per-route content capture (the column stays; nothing
    compiles it).
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
      - Anthropic still goes through its OpenAI-compatible endpoint
        (`https://api.anthropic.com/v1`) with the key as a bearer token. Its
        connection test is native (`GET /v1/models` with `x-api-key` and
        `anthropic-version`). Switching to `schema: Anthropic` with a
        `BackendSecurityPolicy` of type `AnthropicAPIKey` (which aigw
        v1.1.0 has, sending `x-api-key`) is blocked: aigw v1.1.0, and
        ai-gateway's main branch as of 2026-10-05, translate OpenAI chat
        completions to Anthropic only for `GCPAnthropic` and `AWSAnthropic`.
        A `schema: Anthropic` backend serves only Anthropic-style callers
        (`/anthropic/v1/messages`); an OpenAI-style call to it fails with
        "unsupported API schema". Open question for the user: keep the
        OpenAI-compatible endpoint until upstream adds the translator, or
        add a second, native Anthropic backend for Anthropic-style callers.
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
      shared AIGatewayRoutes (ordered, split at 16 rules) while leaving
      other pending edits out, a config nobody reviewed as a whole.
  - Rule order: the gateway tries rules with more header matches first;
    among equals, in rule order. A new route goes ahead of any catch-all
    (`*` with no headers), and two routes can't claim the same model with
    the same headers.
- **Detector thresholds are on hold (decided 2026-09-30).** The detectors
  are regexes (`gateway/detect.go`) with no confidence score, so a
  threshold would change nothing. They wait for real detectors
  (Presidio-style NER, per §5.3). The `detectors` table's threshold,
  `hits_24h` and `fp` are seed values; hit and false-positive counts from
  receipts are a §3 item.

## 7. Cross-cutting

- **Auth and roles (decided 2026-10-05, not built).** Every write still acts
  as `dev@localhost` and no role is enforced yet.
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
    | Draft rules | editor, security |
    | Publish or roll back rules; kill switch; capture | security, admin |
    | Change prices; accept or dismiss price proposals | admin (user, 2026-10-05: prices are shared by every tenant, so a change reprices everyone; restrict to admin when roles are built, not before) |
    | Create, edit or delete budgets | finance, admin |
    | Aliases and routing | editor, admin |
    | Own keys: create, revoke, rotate, extend, finish | the key's owner |
    | Anyone else's keys | admin |
    | Assign roles (Keycloak group mapping) | owner |

    `owner` can do everything. A denied write is a 403 naming the roles
    that may do it (spec §7.6 "Permission denied"), and the console shows
    those controls disabled with that reason.
- **If-Match is required (decided 2026-09-30).** Updating or deleting an
  alias, budget or rule without `If-Match` is a 428; a stale one is a 409
  with the current resource. Creating an alias with `PUT` takes
  `If-None-Match: *`. Dry runs don't need it. Key writes (revoke, rotate,
  extend, finish) and the kill switch don't carry an etag. They are toggles
  that give the same result if repeated, so they don't need one (decided
  2026-10-05). Replacing a provider key follows them (no If-Match, my
  default); editing or deleting a backend takes If-Match like a route.
  Provider writes ("Aliases and routing" in the table above) are editor and
  admin once roles exist.
  - Price writes will require `If-Match` too (decided 2026-10-05, not
    built). Two editors changing the same rate concurrently has happened.
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
