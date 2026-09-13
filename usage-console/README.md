# Nebari Gateway Console

A control plane and operations console for [Envoy AI Gateway](https://aigateway.envoyproxy.io/).

> One endpoint in front of every model, where every request is routed by policy, metered in dollars, and recorded as evidence.

This repo is a hackathon workspace for building the console described in [`docs/spec.md`](docs/spec.md). The spec is the source of truth. Read it before writing code, and keep it updated when a decision changes.

## What we're building

Envoy AI Gateway gives you a provider-agnostic data plane: one OpenAI-compatible API, cross-provider translation, fallback, token-aware rate limiting, quota policy, sealed upstream credentials, and an MCP gateway. What it does not give you is an operations surface. This project builds that surface plus two request-path capabilities the gateway leaves open:

1. **Outbound data protection.** Detect and redact sensitive data before egress, rehydrate on return.
2. **Inbound response inspection.** Treat model output as untrusted: prompt-injection artifacts, rogue tool calls, exfiltration patterns.

Three audiences read the same request stream through different lenses:

| User | Primary job |
|---|---|
| Platform / infra engineer | Keep it up, route correctly, debug a bad call |
| Finance / FinOps | Know where the money went, cap it |
| Security / compliance | Prove what left the perimeter |

## Architecture at a glance

- **Console UI** — React 19, TypeScript, Tailwind v4, nebari-design. Talks to the control plane over REST + SSE with a typed client generated from OpenAPI.
- **Control plane (Go)** — API server, reconciler (server-side apply to AI Gateway CRDs), policy compiler, snapshot service, receipt query.
- **Warden (Go)** — the Envoy `ext_proc` filter. The only new component in the request path.
- **Postgres** for config, keys, and audit. **Postgres + TimescaleDB** for request receipts, fed by an OTel Collector.

Full component map, resource-ownership model, and data model are in spec §4 and §5.

## Delivery phases

| Phase | Focus | Exit criterion |
|---|---|---|
| 0 — Spine | Receipt schema, OTel pipeline, Warden skeleton, API scaffold, OIDC | Real traffic produces queryable receipts |
| 1 — Observe | Traffic, receipt detail, Overview, Spend (read-only) | An engineer would rather debug here than in `kubectl logs` |
| 2 — Configure | Reconciler, backends, routes, keys, budgets, diff-before-apply, export to YAML | Add and route to a provider without touching the cluster |
| 3 — Enforce | Detectors, policy compiler, Warden enforcement, monitor mode, rule builder, replay | A non-engineer authors, replays, and promotes a redaction rule |
| 4 — Optimize | Model aliases, savings analysis, budget enforcement, activity timeline, signed export | A budget cap set in the UI actually stops spend |

The riskiest work is in the request path, so it goes first. Nothing about the UI can be validated on fabricated data.

## Ground rules

- **The console must never become the only way to operate the gateway.** Every console-owned resource exports to YAML. Deleting the console leaves a working gateway.
- Anything marked **VERIFY** in the spec is an assumption. Test it against Envoy AI Gateway 1.x before depending on it.
- Read spec §13 (implementation gotchas) before touching buffers, pricing, streaming token counts, or session IDs.

## Repo layout

```
docs/spec.md    The specification. Start here.
```

Application code lands in follow-up commits as the hackathon progresses.
