# Nebari Gateway Console

**A control plane and operations console for Envoy AI Gateway.**

Spec v0.1 · Draft for review

---

## 0. Document status

This is a design-complete specification for a v1 that includes the policy engine. Anything marked **VERIFY** is an assumption that must be tested against Envoy AI Gateway v1.x before it hardens into a dependency.

**Internal codename:** Project Stargate. Internal use only — it does not appear in the UI, the repo name, docs, or anything published.
**Shipping name:** Nebari Gateway Console.
**Name in the UI:** Gateway.
**Request-path filter:** Warden.

---

## 1. Summary

Envoy AI Gateway v1.0 provides a stable, provider-agnostic data plane: one OpenAI-compatible API across many providers, cross-provider translation, model virtualization, provider fallback, token-aware rate limiting, quota policy, upstream auth with sealed provider credentials, and an MCP gateway. Its control-plane CRDs (`AIGatewayRoute`, `AIServiceBackend`, `BackendSecurityPolicy`, `GatewayConfig`, `MCPRoute`) are declared stable within the 1.x series.

What it does not provide is the thing organizations actually buy: an operations surface. There is no console for watching traffic, attributing spend, authoring data-protection policy, or producing per-request evidence. Commercial products in this space (gate.dev among them) are largely that surface wrapped around equivalent primitives.

This project builds the surface, plus the two engine capabilities the gateway deliberately leaves open:

1. **Outbound data protection.** Detect and redact sensitive data before egress, rehydrate on return.
2. **Inbound response inspection.** Treat model output as untrusted: prompt-injection artifacts, rogue tool calls, exfiltration patterns.

Both are implemented as a single external processing service in the request path. Everything else is UI, API, and a reconciler.

### The one-sentence product

One endpoint in front of every model, where every request is routed by policy, metered in dollars, and recorded as evidence.

---

## 2. Goals and non-goals

### Goals

- **G1** Operate an Envoy AI Gateway deployment entirely from a web console, without `kubectl`.
- **G2** Support *both* dynamic configuration (console/API, database-backed) and declarative configuration (GitOps YAML) in the same cluster, with ownership that is always visible and never ambiguous.
- **G3** Produce a durable, queryable per-request receipt: identity, routing decision, tokens, cost, policy verdicts, redactions.
- **G4** Let a non-engineer author a data-protection rule, see what it would have done to real recorded traffic, and ship it.
- **G5** Attribute every dollar to a team, project, key, and model.
- **G6** Add no more than 10ms p50 of control-plane-attributable overhead on the request path.

### Non-goals

- **N1** Not an endpoint agent. No browser extension, no laptop DLP. This governs machine-to-model traffic.
- **N2** Not a replacement for Envoy Gateway or Envoy AI Gateway. The console is additive and removable; deleting it leaves a working gateway.
- **N3** Not a model training, evaluation, or prompt-management product.
- **N4** No multi-cluster federation in v1. One console, one cluster.
- **N5** No billing or invoicing. Cost attribution and export only.

### Explicit anti-goal

**The console must never become the only way to operate the gateway.** Every console-owned resource can be exported to YAML and handed to Git. A team that outgrows the console should be able to leave without rebuilding configuration by hand. This constraint shapes the data model (§4.3) and shows up as a first-class UI affordance (§7.5.6).

---

## 3. Users and jobs

Three audiences read the same request stream through different lenses. This is the central product insight and it drives the information architecture.

| User | Primary job | What they need from a request |
|---|---|---|
| **Platform / infra engineer** | Keep it up, route correctly, debug a bad call | Latency, status, route decision, fallback events, upstream error body |
| **Finance / FinOps** | Know where the money went, cap it | Tokens, cost, team/project attribution, trend, budget headroom |
| **Security / compliance** | Prove what left the perimeter | Policy verdicts, redactions, destination region, immutable receipt |

Secondary: **application developer**, who touches the console twice — once to get a key, once to debug why their call was blocked. Their experience is mostly onboarding and the receipt detail view.

### Job stories

- When a support bot's bill triples overnight, I want to see which key, which model, and which day it started, so I can cap it before the month closes.
- When legal asks whether customer PII ever reached a US provider, I want to answer with evidence rather than an assurance.
- When my request returns a 403 from the gateway, I want the console to tell me which rule blocked it and what I would need to change.
- When I want to add a new provider, I want to do it without opening a pull request against an infrastructure repo I do not have write access to.

---

## 4. Architecture

### 4.1 Component map

```
┌──────────────┐
│  Console UI  │  React 19 · TS · Tailwind v4 · nebari-design
└──────┬───────┘
       │ REST + SSE (typed client from OpenAPI)
┌──────▼─────────────────────────────────────────────┐
│  Control plane (Go)                                │
│  ├─ API server        REST, OpenAPI 3.1, ETag      │
│  ├─ Reconciler        SSA → AI Gateway CRDs        │
│  ├─ Policy compiler   rules → filter bundle        │
│  ├─ Snapshot service  versioned config → ext_proc  │
│  └─ Receipt query     reads from receipt store     │
└──┬──────────────┬──────────────────┬───────────────┘
   │              │                  │
┌──▼────────┐  ┌──▼─────────────┐  ┌─▼──────────────┐
│ Postgres  │  │ Kubernetes API │  │ Postgres +     │
│ (config,  │  │ (CRDs)         │  │ TimescaleDB    │
│  keys,    │  └──┬─────────────┘  │ (receipts)     │
│  keys,    │  └──┬─────────────┘  └─▲──────────────┘
│  audit)   │     │                  │
└───────────┘     │                  │ OTLP
                  │                  │
       ┌──────────▼──────────┐   ┌───┴───────────────┐
       │ Envoy AI Gateway    │   │ OTel Collector    │
       │ controller          │   └───▲───────────────┘
       └──────────┬──────────┘       │
                  │ xDS              │ traces + access logs
           ┌──────▼──────────────────┴────┐
           │  Envoy proxy (data plane)    │
           │    └─ ext_proc: Warden (Go)  │
           └──────────────┬───────────────┘
                          │
                  Providers · self-hosted models
```

**Warden** *(proposed)* is the external processor. It is the only new component in the request path and the only place where a bug causes a user-visible outage. Treat it accordingly (§9.3).

### 4.2 Deployment topology

Delivered as a Helm chart, packaged as a Nebari pack. Namespace `nebari-gateway`.

- `console` — UI, served as static assets behind the API server
- `control-plane` — API + reconciler, 2 replicas, leader election on the reconciler
- `warden` — ext_proc, sidecar or standalone Deployment (see §4.5 for the tradeoff), HPA on request rate
- `postgres` — CloudNativePG or external
- `receipts` — Postgres with the TimescaleDB extension. Separate instance from the config database (§4.6), same operational skill set
- `otel-collector` — receives OTLP from the gateway, exports to the receipt store

Dependencies assumed present: Envoy Gateway ≥1.8, Envoy AI Gateway ≥1.0, cert-manager, an ingress with TLS.

### 4.3 Resource ownership: the split

Not everything belongs in etcd. Split by churn rate and cardinality.

**Kubernetes CRDs** — infrastructure-shaped, low churn, tens of objects:

| Resource | Why CRD |
|---|---|
| `AIServiceBackend` | Provider endpoint definition, genuinely infrastructure |
| `BackendSecurityPolicy` | Provider credentials, wants Secret references and cloud OIDC |
| `AIGatewayRoute` | Routing topology, benefits from the gateway controller's validation |
| `GatewayConfig`, `Gateway`, `Backend` | Pure infrastructure |
| `MCPRoute` | Same shape as routing |
| `BackendTrafficPolicy` | Rate limits that the gateway enforces natively |

**Postgres** — product-shaped, high churn, thousands to millions of rows:

| Resource | Why not CRD |
|---|---|
| Virtual API keys | One etcd object per key does not scale and puts RBAC in the path of a routine action |
| Teams, projects, members | Product identity, not infrastructure |
| Budgets and spend counters | Mutated on every request |
| Policy rules | Versioned, drafted, dry-run before publish; needs history |
| Model catalog + pricing | Changes when a provider changes a price |
| Receipts | Volume |
| Audit log | Append-only, queried by time |

The rule of thumb: **if a product manager would expect to change it without a deploy, it lives in Postgres.**

### 4.4 Provenance and reconciliation

Every CRD-backed resource carries a provenance value derived from the server-side apply field manager, not from a bespoke annotation:

- **`console`** — field manager is `nebari-gateway-console`. Editable in the UI. Reconciled from Postgres. Drift is reverted on the next reconcile loop and reported.
- **`declarative`** — another field manager owns it (Argo CD, Flux, a human with `kubectl`). Projected read-only into Postgres by an informer. The UI renders it with a provenance badge and a source link.
- **`adopted`** — was `declarative`, explicitly taken over by an audited console action.

Reconcile loop:

1. Informer watches AI Gateway CRDs cluster-wide (or in a configured namespace set).
2. For `console` resources: desired state comes from Postgres. Apply with SSA, `force: false`. A conflict means someone else claimed a field; surface it as drift rather than fighting over it.
3. For `declarative` resources: project spec + status into a read-only mirror table so the console can render them in one list with console-owned resources.
4. Status flows back to the UI over SSE. Every resource row shows a live reconcile state.

**Adoption** is a deliberate action with a confirmation step that explains the consequence: "Git will no longer control this backend. Your next Argo sync will show it as out of sync unless you remove it from the repo." Adoption writes an audit record. There is no implicit adoption on first edit.

**Release** is the inverse: export current state to YAML, hand it to the user, mark the resource `declarative`, stop reconciling.

### 4.5 Warden: the external processor

One Go service implementing the Envoy `ext_proc` gRPC API, registered per-route via the AI Gateway's filter configuration. **VERIFY** the supported extension point and ordering relative to the AI Gateway's own processor before building against it.

Responsibilities, in order:

**Request path**

1. Resolve the virtual key from the `Authorization` header → tenant, team, project, allowed models, budget state.
2. Reject early on: unknown key, revoked key, model not allowlisted, budget exhausted, region not permitted.
3. Run detectors over the request body: PII, secrets, source code, custom regex/entity rules.
4. Apply the matched rule's action: `allow`, `redact`, `reroute`, `block`.
5. On `redact`, substitute stable placeholder tokens (`⟦PERSON_1⟧`), store the mapping in a request-scoped in-memory vault keyed by request ID, and set a TTL.
6. Emit routing hints as headers for the AI Gateway's model virtualization to consume.

**Response path**

7. Rehydrate placeholders in the response if the policy specifies rehydration.
8. Run inbound detectors: injected instruction patterns, tool calls that were not offered, URLs matching exfil heuristics, classification markers.
9. Apply the inbound action: `allow`, `strip`, `block`.
10. Emit the receipt as a structured OTLP log record with the full decision trace.

**Streaming.** SSE responses must be inspected incrementally. Inbound detection on a streamed response cannot buffer to completion without destroying time-to-first-token. Design: a sliding window scanner that can cut the stream mid-flight and emit a terminal error event. The UI must represent a *truncated* response as a distinct state from a *blocked* one.

**Config delivery.** Warden holds a local cache of a versioned snapshot (keys, rules, budgets, catalog) pulled from the control plane, refreshed by a streaming watch. If the control plane is unreachable, Warden serves from cache. Cache age is exported as a metric and rendered in the UI.

**Fail mode** is configurable per policy and must be an explicit, visible choice:

- `fail-open` — control plane down, requests pass unpoliced. Default for cost/routing policies.
- `fail-closed` — control plane down, requests rejected. Default for data-protection policies.

The UI never lets someone create a policy without answering this. The Overview screen shows a banner when any policy is in a degraded fail mode.

**Deployment shape.** Sidecar gives lowest latency and blast-radius isolation per gateway pod; standalone gives independent scaling and a smaller memory footprint at the cost of a network hop. Start with sidecar; revisit if memory per gateway pod becomes a problem.

### 4.6 Telemetry and the receipt pipeline

Envoy AI Gateway already records OpenAI Chat Completions and Embeddings calls as spans with GenAI attributes, exports Prometheus metrics on the admin port, and can auto-wire OTLP access logging when an OTLP endpoint is configured. It also correlates spans and logs by `session.id` when clients send an `agent-session-id` header.

Pipeline:

```
Envoy + AI Gateway  ──OTLP──▶  Collector  ──▶  Timescale  ──▶  Control plane query API
Warden              ──OTLP──▶      │
                                   └────────▶  Prometheus (metrics only)
```

Receipts are assembled in the collector by joining the gateway's span with Warden's decision record on trace ID. Warden owns the fields the gateway cannot know (policy verdicts, redaction counts, resolved identity); the gateway owns timing, tokens, and upstream detail.

**Retention.** Two tiers, configurable:
- Hot (full body hashes, full decision trace, no raw prompt content by default): 30 days
- Cold (aggregates + verdict summary, no per-request detail): 7 years

**Storage design.** Receipts live in Postgres with the TimescaleDB extension, in an instance separate from the config database. Separation matters: receipt writes are high-volume and append-only, config writes are low-volume and transactional, and they should not contend for the same buffers or share a backup schedule.

- `receipts` is a hypertable partitioned on `ts`, chunked daily.
- Native compression after 7 days, segmented by `tenant_id` and ordered by `ts` descending. Receipts are wide and repetitive, so expect a large reduction; measure it in Phase 0 rather than assuming a number.
- Continuous aggregates back every chart in the product. At minimum: spend and token counts by tenant/team/key/model at 5-minute, hourly, and daily grain, plus verdict counts at the same grains. No user-facing chart queries raw receipts.
- Retention policy drops raw chunks at the hot-tier boundary. The daily continuous aggregates are the cold tier and are never dropped.
- Replay (§7.5.7) reads raw receipts, so the hot window is also the maximum replay window. Say so in the UI.

The cost of this choice is that very wide ad-hoc scans over long windows will be slower than a columnar store would be. That is acceptable because the product's queries are known in advance and served by aggregates. Revisit only if ad-hoc analytical exploration becomes a real user need.

Raw prompt/response capture is **off by default**, per-route opt-in, and marked with a loud, persistent indicator in the UI wherever such a route appears. This is a legal landmine and the interface should treat it like one.

---

## 5. Data model

### 5.1 The request receipt

The receipt is the atom of this product. Every screen is a projection of it. Design it first; the views fall out.

```
Receipt
  id                    uuid
  trace_id              string
  session_id            string?          # from agent-session-id
  ts                    timestamptz
  duration_ms           int
  ttft_ms               int?             # streaming only

  # identity
  tenant_id             uuid
  key_id                uuid
  team_id               uuid?
  project_id            uuid?
  actor                 string?          # end-user id, if forwarded

  # routing
  requested_model       string           # what the client asked for
  resolved_model        string           # what actually ran
  backend               string           # AIServiceBackend name
  provider              enum
  region                string?
  route_reason          enum             # alias | policy | fallback | explicit
  fallback_from         string?
  route_trace           jsonb            # ordered decisions with inputs

  # usage
  input_tokens          int
  cached_input_tokens   int
  output_tokens         int
  reasoning_tokens      int
  total_tokens          int
  cost_usd              numeric(18,8)
  cost_basis            jsonb            # price snapshot used, for reproducibility

  # policy
  verdict               enum             # allowed | redacted | rerouted | blocked | truncated
  rules_evaluated       jsonb            # [{rule_id, version, matched, action, ms}]
  redactions            jsonb            # [{type, count}]  — types only, never values
  inbound_verdict       enum
  inbound_findings      jsonb

  # outcome
  status                int
  error_code            string?
  error_detail          string?

  # evidence
  request_hash          string           # sha256 of canonicalized body
  response_hash         string
  content_captured      bool
  receipt_signature     string?          # optional signing key
```

`cost_basis` matters more than it looks. Prices change. A receipt from March must reconcile to March's prices, not today's. Snapshot the price row at write time.

### 5.2 Control-plane schema (abbreviated)

```
tenants(id, name, slug, created_at)
teams(id, tenant_id, name, cost_center)
projects(id, team_id, name)
users(id, tenant_id, email, role)          # role: owner|admin|editor|viewer|finance|security

api_keys(id, tenant_id, team_id, project_id, name,
         hash, prefix, created_by, expires_at, revoked_at,
         allowed_models[], allowed_regions[], budget_id)

budgets(id, scope_type, scope_id, period, cap_usd,
        on_exceed enum(warn|throttle|block), current_usd, resets_at)

model_catalog(id, provider, model_id, display_name, family,
              context_window, modalities[], deprecated_at)
model_pricing(id, model_catalog_id, input_per_mtok, output_per_mtok,
              cached_input_per_mtok, reasoning_per_mtok,
              effective_from, effective_to)
model_aliases(id, tenant_id, alias, target_model_id, conditions jsonb)

policies(id, tenant_id, name, description, status enum(draft|active|disabled),
         fail_mode enum(open|closed), version, created_by, published_at)
policy_rules(id, policy_id, ordinal, match jsonb, action jsonb, enabled)
policy_versions(id, policy_id, version, snapshot jsonb, published_at, published_by)

resources(id, tenant_id, kind, name, namespace, provenance,
          desired jsonb, observed jsonb, sync_state, last_error,
          field_manager, source_ref)       # source_ref: git URL for declarative

audit_log(id, tenant_id, actor_id, action, target_kind, target_id,
          before jsonb, after jsonb, ts, request_id)
```

`audit_log.request_id` and `receipts.trace_id` share an actor identity space. This is what makes §7.5.9 possible.

### 5.3 Policy rule shape

Rules are stored structured, not as text. The UI renders them; a text form is a view, not the source.

```json
{
  "when": {
    "all": [
      { "field": "prompt", "op": "contains_entity", "value": ["email", "ssn"] },
      { "field": "key.team", "op": "not_in", "value": ["security"] }
    ]
  },
  "then": [
    { "action": "redact", "entities": ["email", "ssn"], "rehydrate": true },
    { "action": "reroute", "to": "eu-private" }
  ],
  "else": [],
  "fail_mode": "closed"
}
```

Compiled to a filter bundle Warden can evaluate without allocation per request. Detectors are pluggable: regex, Presidio-style NER, Luhn for card numbers, entropy-based secret detection, and a custom entity type registry.

**Ordering semantics must be decided and documented in v1.** Proposal: rules within a policy evaluate in ordinal order; first `block` wins and short-circuits; `redact` actions accumulate; `reroute` is last-write-wins with a conflict warning shown at authoring time, not runtime.

---

## 6. Control-plane API

- REST, OpenAPI 3.1, typed TS client generated into the frontend at build time.
- `/api/v1/{tenant}/...` scoping. Tenant resolved from session, not from the path alone.
- Optimistic concurrency on every mutable resource via `ETag` / `If-Match`, mapped to the Postgres row version or the CRD `resourceVersion`. A 409 renders as a merge UI, never a silent overwrite.
- SSE endpoints for live surfaces: `/stream/traffic`, `/stream/resources`, `/stream/spend`. Server-side filtering; the client never receives traffic it did not ask for.
- `dryRun=true` supported on policy, route, and budget mutations. Returns the CRD diff and, for policies, the replay result (§7.5.7).
- Every mutation writes an audit record in the same transaction. No exceptions, no best-effort logging.

Auth: OIDC against Keycloak. Roles map to the `users.role` enum. Service accounts get scoped tokens for CI.

---

## 7. Experience design

This is the product. The gateway is a dependency.

### 7.1 Principles

**1. The receipt is the atom.** Overview, traffic, spend, and audit are four projections of one object. If a number on a chart cannot be drilled to the receipts that compose it, it does not ship.

**2. Every decision explains itself.** A block, a reroute, a redaction, a fallback: each carries a trace the user can open inline. "Blocked" without "by which rule, on which match" is a support ticket waiting to happen.

**3. Provenance is always visible.** A user must never discover that a field is read-only by clicking it. Ownership is rendered before interaction, with the exit route attached.

**4. Config changes are proposals until applied.** Show the diff. Pending is a real state with its own visual treatment, not a spinner.

**5. Live, but calm.** Streaming data that thrashes is unreadable. Numbers settle; they do not flicker. Rows enter; they do not reflow the table beneath the cursor.

**6. Cost travels with latency.** Dollars appear next to milliseconds and tokens on every surface where a request appears. Cost is not a separate department.

### 7.2 Visual direction

The subject is infrastructure under observation: money and data moving through a checkpoint in real time, with a legal record attached. The audiences are an SRE at 2am, a finance lead in a spreadsheet mindset, and a compliance officer who needs to trust what they read.

That points away from the marketing-dashboard idiom (dark hero, neon accent, glowing cards) and toward something closer to an instrument panel or a ledger: dense, high-contrast, quiet, with color reserved almost entirely for state.

Design direction for review:

- **Palette.** Near-monochrome working surface in neutral OKLCH ramps, with saturated hue used *only* for verdict and sync state. Five semantic colors, no more: allowed, redacted, rerouted, blocked, degraded. If a color appears and does not mean one of those, it is decoration and should be cut. This deliberately leaves the palette looking sparse; that is the point — on a live traffic screen, a color must be scannable at a glance across 200 rows.
- **Type.** Two families: one for interface text, one monospace for identifiers, model names, tokens, money, and hashes. Tabular numerals mandatory everywhere a number can change without the user acting. Money always right-aligned, always the same number of decimal places within a column.
- **Density.** Three density modes (comfortable / compact / dense) persisted per user. The traffic table defaults to dense; the onboarding flow to comfortable. An SRE and a first-time developer need different products.
- **Structure.** Borders and rules encode grouping; they are not applied uniformly for style. No card-per-section layout. The traffic and spend surfaces are tables and charts on a shared canvas, not a grid of tiles.
- **Motion.** One orchestrated moment per surface, at most. New traffic rows get a brief state-change highlight and nothing else — no slide, no fade-in, no stagger. Motion answers a user action (opening a receipt, expanding a diff, confirming an apply) and otherwise stays out of the way. `prefers-reduced-motion` disables all non-essential motion, and the live-traffic highlight degrades to a static left-edge marker. Follow the motion standards already defined in nebari-design's `AGENTS.md`.

Reviewing this against the generic default: the temptation here is a dark console with a neon-green "live" pulse and rounded stat cards, which is what every observability product ships and what an AI would produce for this brief unprompted. The instrument/ledger direction is the deliberate departure — the boldness budget is spent entirely on verdict color and numeric typography, and everything else stays quiet.

### 7.3 Voice

- Name things by what the user understands. "Provider key", not "BackendSecurityPolicy". The CRD kind appears only in the generated-config drawer and in error detail.
- Buttons name their consequence and keep that name through the flow. "Apply changes" → toast "Changes applied". Never "Submit".
- Errors state what happened and what to do. Never apologize, never be vague. "This key can't reach `claude-opus-4-1`. Add it to the key's allowed models, or route through the `research` key."
- Empty states are invitations with one action. "No traffic yet. Point an app at the gateway →"
- Sentence case throughout. No all-caps labels. No eyebrow text above headings.

### 7.4 Information architecture

```
Overview
Traffic          → Request detail (drawer, deep-linkable)
Spend            → Budgets
Models           → Catalog · Aliases · Pricing
Routing          → Routes · Backends · Fallback
Guardrails       → Policies · Rules · Detectors · Replay
Keys             → Keys · Teams · Projects
Activity         → Config changes + traffic on one timeline
Settings         → Providers · Retention · Integrations · Members
```

Global elements present on every screen:

- **Tenant/environment switcher.** Production vs staging must be unmistakable. Different accent treatment per environment, set in settings, not just a dropdown label.
- **Time range control.** One control, one state, shared across Overview, Traffic, Spend, and Activity. Changing it on one surface and navigating to another preserves it.
- **Command palette** (`⌘K`). Jump to a key, a model, a rule, a trace ID. Paste a trace ID anywhere and it resolves to a receipt.
- **Degradation banner.** Warden cache stale, control plane unreachable, a policy in fail-open, a provider failing over. One banner slot, ranked by severity.

### 7.5 Screens

#### 7.5.1 Onboarding

The migration is a base URL and a key swap. The onboarding must be shorter than the migration.

```
┌────────────────────────────────────────────────┐
│ Connect your first provider                    │
│                                                │
│  ○ OpenAI   ○ Anthropic   ○ Bedrock            │
│  ○ Azure    ○ Vertex      ○ Self-hosted        │
│                                                │
│  API key  [••••••••••••••••••]                 │
│           Tested once, then sealed. Never      │
│           returned to callers.                 │
│                        [ Test connection ]     │
└────────────────────────────────────────────────┘
        ↓ on success
┌────────────────────────────────────────────────┐
│ Your gateway key                               │
│                                                │
│  ngw_live_7f3a…                    [ Copy ]    │
│                                                │
│  - base_url = "https://api.openai.com/v1"      │
│  + base_url = "https://gw.example.com/v1"      │
│  - api_key  = OPENAI_API_KEY                   │
│  + api_key  = NEBARI_GATEWAY_KEY               │
│                                    [ Copy ]    │
│                                                │
│  ◌ Waiting for your first request…             │
└────────────────────────────────────────────────┘
```

The waiting state is live and resolves itself. When the first request lands, the panel replaces itself with that request's receipt. The user's first view of the product is their own traffic, not a tour.

Language tabs on the code block (Python / TS / curl), remembered per user.

**Demo tenant.** Ship a seeded tenant with a week of synthetic traffic, several teams, one blown budget, and a handful of blocked requests. It is reachable from the empty state and clearly marked. Nobody evaluates a traffic console with zero traffic.

#### 7.5.2 Overview

Not a tile grid. A single vertical narrative for the selected time range.

1. **Status strip.** Gateway health, provider health with failover state, Warden cache age, policy fail modes. Green is the absence of information; anything not nominal is stated in words.
2. **Traffic sparkline** with verdict composition as a stacked area. Blocked and redacted are visible at this altitude, because that is the point of the product.
3. **Three numbers with trend**: requests, spend, blocked+redacted. Each is a link to its surface with the time range carried across.
4. **What changed.** Config changes in this window, joined to their traffic effect. "Route `default` switched to `claude-sonnet-5` at 14:02 — p50 latency −340ms, cost/request −38%."
5. **Attention list.** Budgets over 80%, keys with anomalous spend, rules that fired far more than their 7-day baseline, providers that failed over. Each row has one action.

Item 4 is the differentiating screen in the product and is worth disproportionate design effort.

#### 7.5.3 Traffic

A dense virtualized table over a live stream.

```
┌───────────────────────────────────────────────────────────────────┐
│ [Live ●]  last 1h ▾   key ▾  team ▾  model ▾  verdict ▾  + filter │
├──┬──────────┬─────────┬──────────────┬──────┬───────┬──────┬──────┤
│  │ time     │ key     │ model        │ tok  │ cost  │  ms  │      │
├──┼──────────┼─────────┼──────────────┼──────┼───────┼──────┼──────┤
│▌ │ 14:02:11 │ support │ gpt-5-mini   │ 1.2k │ $.003 │  412 │  ✓   │
│▌ │ 14:02:10 │ agents  │ sonnet-5     │ 8.4k │ $.021 │ 1203 │  ⊘   │  ← redacted
│▌ │ 14:02:10 │ batch   │ opus-4-1 ↓   │ 22k  │ $.340 │ 4021 │  ✓   │  ← fell back
│▌ │ 14:02:09 │ web     │ gpt-5.5      │  —   │   —   │   38 │  ✕   │  ← blocked
│◌ │ 14:02:09 │ agents  │ sonnet-5     │ 1.1k↑│   —   │  ttft│  ⋯   │  ← streaming
└──┴──────────┴─────────┴──────────────┴──────┴───────┴──────┴──────┘
```

Behaviors that make or break this screen:

- **Freeze on interaction.** Hovering or focusing the table pauses insertion and shows "N new" as a click-to-resume affordance. Rows never move under the pointer.
- **In-flight rows.** Streaming requests appear immediately with unknown token counts and settle in place when usage arrives at the end of the stream. The row must not change height when it settles — reserve the space.
- **Verdict is the leftmost signal**, as a color bar at the row edge, readable in peripheral vision while scrolling.
- **Filters are the query.** The filter bar serializes to the URL. Every filtered view is shareable, and "share this view" is a visible action.
- **Backpressure.** Above a threshold rate, the stream switches to sampled mode with an explicit, honest indicator: "Sampling 1 in 20. Add a filter to see everything matching." Never silently drop.
- **Columns** are user-configurable and persisted, with pinning on time and key.
- Row click opens the receipt drawer without losing stream position.

#### 7.5.4 Request receipt

A drawer, deep-linkable as a page, printable, exportable as signed JSON.

Sections, in this order:

1. **Header.** Verdict, model resolution (`requested → resolved`), status, total cost, total duration. Trace ID with copy.
2. **Decision trace.** A vertical sequence showing what happened in order: identity resolved → budget checked → rules evaluated → route selected → upstream called → response inspected. Each step shows its input, its outcome, and its latency contribution. This is the most important component in the product.
3. **Policy detail.** Rules evaluated, which matched, what each did. Redactions shown as type and count only — never the matched values, which would recreate the leak inside the audit tool.
4. **Usage and cost.** Token breakdown with the price basis that produced the number, stated explicitly.
5. **Content.** Present only if capture was enabled for this route. Collapsed by default, behind an explicit reveal that writes its own audit record.
6. **Related.** Same session, same key in the last hour, the config change most recently preceding this request.

#### 7.5.5 Spend and budgets

Two modes on one screen, toggled: **trend** (time series, stacked by a chosen dimension) and **breakdown** (sortable table by team, project, key, model, provider).

- Every cell drills through to the filtered traffic view.
- Budget rows show consumption against cap with the enforcement action stated as words: "Blocks new requests at $40,000" not a progress bar alone.
- Projection to period end, with the basis shown. A forecast without its assumptions is a guess wearing a suit.
- Savings analysis: requests where a cheaper model in the same family would plausibly have served, expressed as "$X/mo if `summarize-*` moved to `gpt-5-mini`", with a link to the receipts it is based on and a one-click path to a *draft* alias change (never an auto-apply).
- Export: CSV and a PDF that a finance team can attach to a close.

#### 7.5.6 Models, routing, and backends

One list surface with provenance as a first-class column.

```
┌──────────────────────────────────────────────────────────┐
│ Backends                              [ + Add provider ] │
├──────────────────────────────────────────────────────────┤
│ ● openai-prod      OpenAI      Console    Synced         │
│ ● anthropic-prod   Anthropic   Console    Applying…      │
│ ● bedrock-eu       Bedrock     Git ↗      Synced         │
│ ● vllm-internal    Self-hosted Git ↗      Drift detected │
└──────────────────────────────────────────────────────────┘
```

Provenance behavior:

- **Console** rows are editable. Edit opens a form; save shows a CRD diff before applying.
- **Git** rows are read-only with the badge linking to the source file. The detail view offers **Adopt into console**, which explains the consequence before proceeding.
- **Drift** on a console-owned resource shows what changed, who changed it, and offers *revert to desired* or *accept as new desired*.
- Every row, regardless of provenance, offers **View generated YAML** and **Export**.

Export-to-YAML on console-owned resources is not a nice-to-have. It is the mechanism by which the console avoids becoming a trap, and it should be as prominent as Save.

Routing gets a visual editor: request conditions on the left, model targets on the right, fallback chains as ordered lists. It compiles to `AIGatewayRoute`. Complex routes that the visual editor cannot express fall back to a YAML editor with schema validation, and the UI says so plainly rather than silently mangling them.

#### 7.5.7 Guardrails and the replay

The differentiating surface. Three panes.

```
┌───────────────┬────────────────────────┬───────────────────┐
│ Rules         │ Rule builder           │ Replay            │
│               │                        │                   │
│ 1 no-pii-out  │ When                   │ Against: last 1h  │
│ 2 eu-only     │  ├ prompt contains     │ 4,182 requests    │
│ 3 block-src   │  │   email, SSN        │                   │
│ + new rule    │  └ team is not         │ ✓ allowed  4,139  │
│               │      security          │ ⊘ redacted    41  │
│               │                        │ ✕ blocked      2  │
│               │ Then                   │                   │
│               │  ├ redact email, SSN   │ ─────────────     │
│               │  └ route to eu-private │ Would newly       │
│               │                        │ block 2 requests  │
│               │ If unavailable         │ from `support`:   │
│               │  ◉ block  ○ allow      │  · 13:44 ⟶ view   │
│               │                        │  · 12:02 ⟶ view   │
│               │       [ Test ] [ Save ]│                   │
└───────────────┴────────────────────────┴───────────────────┘
```

**The replay is the feature.** Authoring a rule against a recorded window and seeing exactly which real requests it would have changed is what converts this from a YAML editor with rounded corners into something a compliance officer will use.

Requirements:

- Replay runs the compiled rule against stored receipts using the same evaluator Warden uses. Not an approximation, not a second implementation.
- Replay over captured content is exact. Over hash-only receipts it is necessarily partial, and the UI must say which it is: "Replayed against 4,182 requests. 3,901 had content available; 281 evaluated on metadata only."
- Diff framing: the result is always expressed as *change from current behavior*, not absolute counts. "Would newly block 2" is actionable; "blocks 2" is not.
- Each affected request links to its receipt.
- Publishing is versioned. A published policy version is immutable, diffable against the previous, and rollback is one action.
- Rules may be published in **monitor mode**, where they evaluate and record verdicts but take no action. Every serious policy should start here, and the UI should make monitor mode the default for a rule's first publish.

Detectors get their own management surface: built-in entity types, custom regex with a test box, per-detector confidence thresholds, and a false-positive review queue fed by users marking a redaction as incorrect from the receipt view.

#### 7.5.8 Keys

- Creation collects: name, team, project, allowed models, allowed regions, budget, expiry. Expiry is required; "never" must be an explicit choice with a warning, not the default.
- The secret is shown exactly once, with a copy action and an acknowledgment before dismissal.
- Each key's detail page is its own miniature dashboard: traffic, spend, top models, recent blocks, last used.
- Revocation is immediate and states its blast radius before confirming: "This key made 1,204 requests in the last 24 hours. Revoking stops them now."
- Rotation issues a new secret with an overlap window and shows traffic migrating from old to new, so the user can watch the cutover complete.

#### 7.5.9 Activity

Config changes and traffic on a single timeline, filterable by actor, resource, or effect.

This is possible because audit records and receipts share an actor identity space and a clock. It answers the question every incident review asks: *what changed, and what did it do?* No product in this category currently shows it. Build it.

### 7.6 Cross-cutting states

Every one of these needs a designed treatment in nebari-design, not an ad-hoc implementation per screen:

| State | Treatment |
|---|---|
| Pending apply | Resource dimmed with a distinct pending marker, diff accessible, cancel available |
| Reconcile failed | Inline error with the API server's message verbatim and a retry |
| Drift | Banner on the resource with the field-level diff and two resolutions |
| Read-only (Git) | Provenance badge, source link, adopt action. Never a disabled field with no explanation |
| Stale cache | Global banner with cache age, degraded-behavior explanation |
| Fail-open active | Persistent, non-dismissible while active |
| Sampling active | Inline in the traffic header with the sample rate and how to see everything |
| Content capture on | Persistent marker on every surface where that route appears |
| Loading | Skeletons that match final layout dimensions. No layout shift on resolve |
| Empty | One sentence of orientation plus one action |
| Permission denied | What role is required and who to ask |

### 7.7 Accessibility

- WCAG 2.2 AA as a build gate, not a review item.
- Verdict is never conveyed by color alone: every verdict has a glyph and a text label available.
- The live traffic table is keyboard-navigable and does not steal focus when rows arrive. New rows are announced to screen readers at a throttled rate, or not at all if the user has paused the stream.
- Charts have a table equivalent reachable by keyboard.
- `prefers-reduced-motion` disables all decorative motion and the row-arrival highlight.
- Full keyboard path for every destructive action, with confirmation that cannot be triggered by a single keystroke.

---

## 8. nebari-design work

Components this product forces into the design system. Each lands in Storybook 9 with tests before it is used in the app.

**New primitives**

| Component | Notes |
|---|---|
| `DataTable` | Virtualized, column pinning + resize + reorder, density modes, persisted config |
| `StreamTable` | `DataTable` plus insertion, freeze-on-hover, in-flight rows, backpressure indicator |
| `DiffView` | Unified and split, YAML-aware, used for CRD diffs and policy versions |
| `CodeBlock` | Copy, language tabs, line highlight |
| `DecisionTrace` | Ordered steps with per-step input/outcome/duration. The receipt's spine |
| `ProvenanceBadge` | console / git / adopted, with source link and adopt affordance |
| `SyncStateIndicator` | synced / applying / failed / drift, live-updating |
| `FilterBar` | Structured filters, URL-serialized, typeahead on values |
| `RuleBuilder` | Nested condition tree, action list, keyboard-operable |
| `Drawer` | Deep-linkable, stacking, focus-trapped |
| `TimeRangePicker` | Absolute + relative, shared global state |
| `CommandPalette` | ⌘K, with a resolver registry |
| `Money`, `TokenCount`, `Duration` | Tabular numerals, locale-aware, consistent precision |
| `Sparkline`, `StackedArea`, `BarSeries` | Themed on OKLCH tokens, with keyboard-accessible table fallback |

**New tokens**

- Verdict ramp: allowed / redacted / rerouted / blocked / degraded, each with a foreground, a background, and a border tint at AA contrast in both themes.
- Sync-state ramp.
- Density scale (three steps) applied to table row height, cell padding, and control size.
- Chart series ramp derived from the existing OKLCH ramps, tested for categorical distinguishability under the common color-vision deficiencies.

**Standards to extend**

- Motion guidance in `AGENTS.md` needs a section on continuously-updating surfaces. The existing rules assume user-triggered transitions; live tables need rules about what is allowed to move when the user is not acting.

---

## 9. Security

### 9.1 Credentials

Provider keys never leave the cluster and are never returned by the API, not even to owners, not even masked beyond a prefix. Stored as Kubernetes Secrets referenced by `BackendSecurityPolicy`, with envelope encryption via the cluster KMS. Cloud providers with an OIDC story use short-lived credential retrieval; providers without one require key rotation reminders surfaced in the UI.

Virtual gateway keys are stored hashed (Argon2id), with a non-secret prefix for identification in logs and UI.

### 9.2 Data handling

- Prompt and response content is not persisted by default. Hashes only.
- Enabling capture is per-route, requires an elevated role, writes an audit record, and marks every affected surface persistently.
- Redaction mappings live only in request-scoped memory with a TTL, never on disk.
- Detected values never appear in receipts, logs, or metrics. Types and counts only.
- Receipt export is audited.

### 9.3 Request-path safety

Warden is the only new component that can take down traffic. It gets:

- A hard evaluation deadline per request (default 50ms) after which the configured fail mode applies.
- Panic recovery that fails to the configured mode rather than dropping the connection.
- No synchronous dependency on Postgres or the control plane in the hot path. Cache only.
- A kill switch: a single flag that puts Warden into pass-through, settable without a rollout.
- Load testing against a body-size distribution that includes multimodal payloads.

### 9.4 Tenancy

Tenant boundary is enforced in the API layer and in row-level security in Postgres, not only in query construction. Cross-tenant reads should fail at the database even if application code is wrong.

---

## 10. Performance targets

| Metric | Target |
|---|---|
| Warden added latency, p50 / p99 | ≤ 3ms / ≤ 15ms, non-streaming |
| Added time-to-first-token, streaming | ≤ 5ms p99 |
| Receipt visible in Traffic after completion | ≤ 2s p95 |
| Traffic table sustained render | 2,000 rows/min without frame drops |
| Spend query, 30d, 10M receipts, via continuous aggregate | ≤ 1.5s p95 |
| Replay, 10k receipts | ≤ 5s |
| Console TTI | ≤ 1.5s on a cold cache |

---

## 11. Delivery

The riskiest work is in the request path, so it goes first. Nothing about the UI can be validated on fabricated data.

**Phase 0 — Spine (4–6 weeks)**
Receipt schema. OTel pipeline into the receipt store. Warden skeleton in the path doing identity resolution and receipt emission only, no enforcement. Control-plane API scaffold, Postgres schema, OIDC.
*Exit:* real traffic through the gateway produces queryable receipts.

**Phase 1 — Observe (4 weeks)**
Traffic, receipt detail, Overview, Spend. Read-only. `DataTable`, `StreamTable`, `DecisionTrace`, chart primitives into nebari-design.
*Exit:* an engineer would rather debug here than in `kubectl logs`.

**Phase 2 — Configure (5 weeks)**
Reconciler with SSA and provenance. Backends, routes, keys, budgets. Diff-before-apply, drift detection, export to YAML, adopt/release.
*Exit:* a provider can be added and routed to without touching the cluster, and a Git-managed resource is visibly, safely read-only.

**Phase 3 — Enforce (6 weeks)**
Detectors, policy compiler, Warden enforcement, monitor mode, rule builder, replay.
*Exit:* a non-engineer authors a redaction rule, replays it against real traffic, publishes in monitor mode, and promotes it.

**Phase 4 — Optimize (4 weeks)**
Model aliases with conditions. Savings analysis. Budget enforcement actions: warn at threshold, throttle via rate-limit policy, hard block, with the enforcement decision visible in the receipt's decision trace. Activity timeline. Signed receipt export.
*Exit:* a budget cap set in the UI actually stops spend, and the requests it stopped are visible with the reason attached.

Onboarding and the demo tenant are built in Phase 1 and revised in every phase after. They are not a launch task.

### 11.1 Follow-on phases

Everything below is post-v1. Sequencing is by dependency and by how much of it can be justified before real users ask, so treat the order as a default rather than a commitment. Each phase should be re-argued against actual usage before it starts.

**Phase 5 — Spend intelligence**
*Depends on: Phase 4 budgets, a few months of receipt history.*

Budget caps stop damage after it happens. This phase is about seeing it coming and settling the bill.

- Anomaly detection on spend and volume, per key and per team, against a learned baseline rather than a static threshold. Surfaced in the Overview attention list, not as a separate alerting product.
- Forecasting with stated assumptions and a confidence range. A projection whose basis is hidden is a liability.
- Chargeback and showback: period close, cost-center allocation, a finance-ready export that reconciles to the provider invoice. Reconciliation is the hard part — your token accounting and the provider's will disagree, and the UI should show the delta rather than pretend it doesn't exist.
- Commitment and discount awareness, so a team on a provider committed-spend deal sees effective cost rather than list price.
- Quota requests as a workflow: a team asks for more budget, an owner approves, the change is audited.

*Exit:* finance closes a month from this console without a spreadsheet.

**Phase 6 — Caching**
*Depends on: Warden being trusted in the path, stable receipt accounting.*

The largest available cost reduction, and the one most likely to justify the whole platform economically.

- Exact-match response cache, keyed on a canonicalized request, with per-route TTL and explicit opt-in. Trivial to build, immediately valuable for deterministic workloads.
- Semantic cache behind an embedding lookup, with a tunable similarity floor. Much higher risk: a wrong hit returns a confidently incorrect answer. It needs per-route enablement, a visible similarity score on every hit in the receipt, and a one-click "this hit was wrong" path that feeds a review queue.
- Provider-native prompt caching awareness, so cached input tokens are priced correctly and cache hit rate is a first-class metric rather than an accounting anomaly.
- Cache surfaces in the UI as a verdict alongside the existing five, with its own color. Savings from caching appear in Spend as a separate line, because a finance lead will ask what the cache is worth.

*Exit:* cache hit rate and dollars saved are on the Overview, and a route can be switched to semantic caching and back without a deploy.

**Phase 7 — Routing intelligence**
*Depends on: Phase 6 embeddings infrastructure, enough traffic history to compare models on real workloads.*

- Quality-aware routing: classify a request and route to the cheapest model that has historically handled that class acceptably. Requires a quality signal, which is the hard dependency, not the routing.
- Shadow traffic: mirror a percentage of production requests to a candidate model, compare cost, latency, and output without affecting the caller. This is the safe path to every model migration and is worth building before quality routing.
- A/B routing with a real experiment surface: split, run, compare, promote or roll back. The comparison view is a genuine design problem and should get its own spec.
- Latency-aware and capacity-aware routing for self-hosted backends, integrating with the Gateway API Inference Extension so routing reflects queue depth rather than a static weight.
- Regression detection on model swaps: when an alias changes target, automatically compare the before and after windows and flag movement in cost, latency, error rate, and policy verdicts.

*Exit:* switching a production workload to a cheaper model is a reviewed decision backed by shadow data, not a guess.

**Phase 8 — Agents and MCP**
*Depends on: session correlation already in the receipt, MCP routing in use.*

The gateway sees MCP traffic but the console treats it as ordinary routes. Agent workloads are where governance gets genuinely hard, and where this product could be differentiated rather than merely complete.

- Session as a first-class object: a multi-turn agent run rendered as one unit with total cost, total duration, the tool calls made, and where it went wrong. Today's receipt is per-request; agents are per-session.
- Tool inventory and per-tool policy: which MCP servers are reachable by which keys, which tools are allowlisted, what each tool call cost.
- Tool-call audit, including calls the model attempted that were not offered to it. The inbound detector already looks for this in Phase 3; this phase gives it a home.
- Loop and runaway detection, with a per-session spend ceiling. An agent stuck in a retry loop is the most common way an AI bill goes vertical.
- Human-in-the-loop approval for designated tools, where a call pauses pending approval in the console.

*Exit:* an agent run can be reviewed end to end, and a runaway session is capped before it costs real money.

**Phase 9 — Compliance and scale**
*Depends on: demand. Do not build speculatively.*

- Evidence packages: scoped, signed, tamper-evident exports for an auditor, generated from receipts rather than assembled by hand.
- Regulatory reporting templates, including the EU AI Act transparency and record-keeping obligations where they apply to deployers.
- Data subject requests: find everything associated with an identifier, across the retention window.
- Four-eyes approval on policy publish and provider changes, with an approval queue. Relevant the moment a regulated customer adopts this.
- SIEM and alerting integrations: webhook, Splunk, Datadog, PagerDuty on policy and budget events.
- Multi-cluster and multi-region federation, lifting non-goal N4. One console, many data planes, receipts aggregated centrally with region-of-processing preserved per request.
- Terraform provider and a CLI, so the console's own resources can themselves be managed declaratively. Pleasingly recursive, and the natural endpoint of the export-to-YAML principle in §2.

*Exit:* driven by a specific customer requirement, not by this list.

### 11.2 Deliberately deferred

Named here so they stop being re-proposed every quarter:

- **Prompt management and versioning.** Adjacent, frequently requested, and a different product. It belongs in the application, not the gateway.
- **Model evaluation harness.** Phase 7 needs a quality signal but should consume one, not build one.
- **Fine-tuning orchestration.** Not a gateway concern.
- **Endpoint or browser DLP.** Non-goal N1 and still true.
- **A guardrail marketplace.** Only interesting once enough tenants have authored enough policies to make sharing worthwhile.

---

## 12. Risks and open questions

| # | Risk | Mitigation |
|---|---|---|
| R1 | ext_proc extension point or ordering differs from assumption | Spike in week 1 against v1.x before any other work |
| R2 | Streaming inbound inspection is not viable without hurting TTFT | Prototype the sliding-window scanner in Phase 0; if it fails, scope inbound inspection to non-streaming in v1 and say so |
| R3 | Receipt volume overruns the store | Sampling policy designed in Phase 0, not retrofitted. Aggregate rollups from day one |
| R4 | Provenance model confuses users despite the design | Usability-test the Git/console split in Phase 2 with someone who has never seen it |
| R5 | Detector false positives erode trust in the policy engine | Monitor mode default, false-positive review queue, per-detector thresholds |
| R6 | Replay fidelity gap on hash-only receipts | State the gap in the UI rather than papering over it |
| R7 | Scope. Phase 3 is the whole product's differentiation and the largest unknown | Phases 0–2 are independently valuable and shippable. Phase 3 can slip without stranding the work |

Open decisions:

- Compression and continuous-aggregate policy tuning on the receipts hypertable. Set in Phase 0 against real volume estimates, revisited once traffic is real.
- Whether MCP routing gets first-class UI in v1 or appears only as routes.
- Receipt signing: nice for evidence claims, adds key management. Defer to Phase 4 unless a design partner needs it sooner.
- Relationship to the existing `llm-serving-pack` work: this either absorbs it or that becomes the provider-configuration slice of Phase 2. Worth settling before Phase 2 starts.

---

## 13. Implementation gotchas

- **Buffer limits.** Envoy Gateway's default buffer limit is 32KiB, which is far too small for LLM traffic. A base64 image or a long conversation history will be rejected. Raise it (the AI Gateway samples use 50MiB) from the first deploy, and add a regression test with a large multimodal body.
- **Price snapshots.** Snapshot the pricing row into each receipt. Reconciling last quarter's spend against this quarter's prices produces numbers no finance team will accept.
- **Session correlation.** The gateway maps an `agent-session-id` header to `session.id` on spans and logs, and deliberately keeps session IDs out of metrics to avoid cardinality blowup. Adopt the same discipline: sessions are a receipt dimension, never a metric label.
- **Route scoping for costs.** `llmRequestCosts` are scoped per `AIGatewayRoute`. Two routes can use the same metadata keys without colliding. Do not assume a global cost namespace.
- **Tokens arrive last.** In streaming responses, usage is known only at the end. Every UI surface showing token counts needs an unknown state, and every aggregate needs to exclude in-flight requests rather than counting them as zero.
- **Reasoning tokens.** These are a separate cost type. Budget and display them separately or the numbers will be wrong for reasoning models.
