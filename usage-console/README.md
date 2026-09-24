# Project Stargate

A hackathon challenge: build a control plane and operations console for [Envoy AI Gateway](https://aigateway.envoyproxy.io/).

> One endpoint in front of every model, where every request is routed by policy, metered in dollars, and recorded as evidence.

> **Naming note:** Envoy AI Gateway is being renamed **Agent Router**. The spec and this README still say "Envoy AI Gateway" throughout. Read the two names as the same project, and expect upstream docs, CRD names, and Helm charts to shift as the rename lands.

**Fork this repo to take part.** It ships with the spec in [`docs/spec.md`](docs/spec.md) and nothing else. What you build on top of it is up to you.

## The challenge

Envoy AI Gateway gives you a provider-agnostic data plane: one OpenAI-compatible API, cross-provider translation, fallback, token-aware rate limiting, quota policy, sealed upstream credentials, and an MCP gateway. What it does not give you is an operations surface. The spec describes one, shipping as the **Nebari Gateway Console**, plus two request-path capabilities the gateway leaves open:

1. **Outbound data protection.** Detect and redact sensitive data before egress, rehydrate on return.
2. **Inbound response inspection.** Treat model output as untrusted: prompt-injection artifacts, rogue tool calls, exfiltration patterns.

Three audiences read the same request stream through different lenses:

| User | Primary job |
|---|---|
| Platform / infra engineer | Keep it up, route correctly, debug a bad call |
| Finance / FinOps | Know where the money went, cap it |
| Security / compliance | Prove what left the perimeter |

## What's fixed and what's up to you

Two things are non-negotiable:

1. **Envoy AI Gateway (Agent Router) is the data plane.** Build on it, don't replace it.
2. **A really nice, genuinely useful UI.** That's the point of the exercise. Judge every decision by whether it makes the console better to use.

Everything else in the spec is a suggested implementation. The design system, the database, the control-plane language, the receipt store, and the telemetry pipeline are all fair game to swap if you have a good reason or just know something else better. Keep the intent of the spec (receipts, ownership visibility, export to YAML) and pick the tools that get you there fastest.

## Architecture at a glance

The spec's suggested shape:

- **Console UI** — React 19, TypeScript, Tailwind v4, nebari-design. Talks to the control plane over REST + SSE with a typed client generated from OpenAPI.
- **Control plane (Go)** — API server, reconciler (server-side apply to AI Gateway CRDs), policy compiler, snapshot service, receipt query.
- **Warden (Go)** — the Envoy `ext_proc` filter. The only new component in the request path.
- **Postgres** for config, keys, and audit. **Postgres + TimescaleDB** for request receipts, fed by an OTel Collector.

Full component map, resource-ownership model, and data model are in spec §4 and §5.

## Delivery phases

The spec lays out five phases. For a hackathon, Phase 0 and Phase 1 are the realistic target. Anything past that is a stretch goal.

| Phase | Focus | Exit criterion |
|---|---|---|
| 0 — Spine | Receipt schema, OTel pipeline, Warden skeleton, API scaffold, OIDC | Real traffic produces queryable receipts |
| 1 — Observe | Traffic, receipt detail, Overview, Spend (read-only) | An engineer would rather debug here than in `kubectl logs` |
| 2 — Configure | Reconciler, backends, routes, keys, budgets, diff-before-apply, export to YAML | Add and route to a provider without touching the cluster |
| 3 — Enforce | Detectors, policy compiler, Warden enforcement, monitor mode, rule builder, replay | A non-engineer authors, replays, and promotes a redaction rule |
| 4 — Optimize | Model aliases, savings analysis, budget enforcement, activity timeline, signed export | A budget cap set in the UI actually stops spend |

The riskiest work is in the request path, so it goes first. Nothing about the UI can be validated on fabricated data.

## Things worth knowing before you start

- **The console must never become the only way to operate the gateway.** Every console-owned resource exports to YAML. Deleting the console leaves a working gateway.
- Anything marked **VERIFY** in the spec is an assumption. Test it against Envoy AI Gateway 1.x before depending on it.
- Spec §13 lists implementation gotchas around buffer limits, price snapshots, streaming token counts, and session IDs. Read it before you hit them the hard way.

## Repo layout

```
docs/spec.md    The specification. Start here.
console/        Front-end mockup of the Gateway console on nebari-design (see console/README.md).
```
