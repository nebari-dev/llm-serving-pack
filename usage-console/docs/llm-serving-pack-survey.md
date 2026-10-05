# llm-serving-pack survey

Survey of [nebari-dev/llm-serving-pack](https://github.com/nebari-dev/llm-serving-pack) at commit `9806bbd` (2026-10-05), for spec §12's open question: does Stargate absorb this pack, or does the pack become the provider-configuration slice of Phase 2? Paths below are in that repo.

## What the pack is

- **Operator** (`operator/`). It has two CRDs in `llm.nebari.dev/v1alpha1`:
  - `LLMModel` serves a model on vLLM: weights from HuggingFace or OCI, GPUs, replicas, an InferencePool with an EPP scheduler.
  - `PassthroughModel` covers external providers: OpenAI-compatible with an API key in a Secret, or Bedrock through workload identity.

  Both CRDs have `access.public` / `access.groups` and external/internal endpoints. Validating webhooks require models to live in the operator namespace.
- **Key-manager** (`key-manager/`). A Go API, authenticated with Keycloak JWTs, that lets a user create, list and revoke their own keys for models their groups can reach.
- **Frontend** (`frontend/`). One page, "LLM API Key Manager": a keys table with create and revoke dialogs. It uses React 19, shadcn with the `@nebari` theme, Base UI and keycloak-js.
- **Packaging**. Helm chart `nebari-llm-serving` 0.1.4, plus an Argo CD example that runs `selfHeal` + `prune` + ServerSideApply. Pinned versions are Envoy Gateway 1.8.1, AI Gateway 1.1.0 (the same as our `aigw`), Gateway API 1.5.1 and GIE 1.4.0.

## Generated gateway resources and who owns them

For each model, the operator writes:
- AIGatewayRoutes `<m>-external` and `<m>-internal`, one rule each, matching `x-ai-eg-model`;
- SecurityPolicies `<m>-{external,internal}-auth`;
- Secret `<m>-api-keys` and ConfigMap `<m>-api-key-metadata`;
- for passthrough models, also Backend, BackendTLSPolicy, AIServiceBackend and BackendSecurityPolicy.

The operator doesn't use server-side apply. Gateway CRs go through Get, then Create or a full-object `Update` that keeps only finalizers and resourceVersion (`operator/internal/controller/unstructured_apply.go`). Every reconcile replaces the whole spec, labels and annotations. The only patch in the codebase is a status merge-patch.

The operator doesn't watch its generated routes or policies; it only owns Deployments, Services and PVCs. So a change someone else makes to a route survives until the next reconcile, then gets wiped. A reconcile is triggered by:
- a CR change;
- any api-keys Secret change, which re-reconciles every model;
- an owned-object event or a requeue.

Two more ownership facts:
- `LLMModel` sets no ownerRef on its routes or policies, so they outlive the model. The repo's install docs admit this causes an ai-gateway-controller crashloop.
- A TLS singleton does a full `Update` on the two Gateways every 5 minutes (`nebari-gateway` and `nebari-internal-gateway` in `envoy-gateway-system`). It can be turned off with `manageSharedListeners=false`.

**For Stargate:** our routing reconciler (spec §4.4, §7.5.6) can't co-write these routes, because server-side apply field ownership means nothing against a full-object Update. Anything we add has to live in objects we own, attached to their routes or Gateways (EnvoyExtensionPolicy, BackendTrafficPolicy), or in AIGatewayRoutes of our own.

## Keys and identity

| | llm-serving-pack | Stargate |
|---|---|---|
| Storage | Plaintext `sk-…` values in one k8s Secret per model; metadata in a ConfigMap | Hashed secrets in Postgres |
| Check | Envoy Gateway SecurityPolicy `apiKeyAuth`, pooled over every `*-api-keys` Secret, deny-by-default allow-list per model | ext_authz → stargate-api |
| Scope | User (the creator string) × model; OIDC groups decide which models a user may mint keys for | Tenant / project / user, plus budgets |
| Expiry, rotation | None. Expiry is an explicit non-goal | `rotate_until`, extend, finish |
| Revocation | Deletes the Secret entry; takes effect when Envoy Gateway syncs the Secret (seconds to about a minute) | Immediate at ext_authz |
| Audit | No record of who created or revoked keys. The "auditor" is meant to revoke keys from users who lost group access, but its group lookup is a stub that always errors, so it never revokes | Audit rows on every write |
| Identity upstream | External endpoint: `x-llm-client-id` only. The client ID looks like `user-<user>-<model>-<hash>-<n>`. Internal endpoint (JWT): `X-Auth-User`, `X-Auth-Groups` | Identity headers → access log → receipts |
| Scale | Each model's Secret is capped at 1 MiB, so a few thousand keys per model | Postgres |

The docs list usage billing, chargeback, per-key rate limits, token quotas, team-shared keys and key expiry as non-goals (`docs/src/content/docs/architecture.mdx:40-44`).

## Gateway configuration Stargate depends on

None of these are configured anywhere in the repo:
- EnvoyProxy, `filterOrder`, buffer limits, BackendTrafficPolicy, ClientTrafficPolicy;
- EnvoyExtensionPolicy or ext_authz;
- rate limits, access logs, OTel sinks.

The docs say the gateway does "token counting and rate limiting", but nothing renders it. Usage data is described as "available for future cost tracking".

The Envoy Gateway and AI Gateway Argo Applications also self-heal, so changing EnvoyProxy (our access-log → OTLP receipts path, the Warden filter-order fix, buffer limits from spec §13) means going through their git overlay (`examples/envoy-gateway-overlay.yaml`). Changes made directly on the cluster get reverted.

Models pass through by exact name: one backendRef per rule, with no weights, fallback or name rewriting. Stargate's aliases and routing have no counterpart in the pack.

## Recommendation

**Treat the pack as the provider and model-serving layer. Don't absorb it.**

Spec §12 offers two options: absorb the pack, or make it the provider-configuration slice of Phase 2. I recommend the second, with Stargate as the control plane above it.

The pack's real value is model lifecycle: vLLM deployment, GPU scheduling, InferencePool/EPP, and passthrough credentials, including Bedrock IRSA. Stargate has none of that and shouldn't rebuild it. The pack in turn has none of what Stargate is for: receipts, spend, budgets, guardrails, aliases, audit, and rotation.

In practice that means:
- Stargate reads `LLMModel` / `PassthroughModel` (status, endpoints, access) for its Models and backends views. Creating or editing them goes through their CRs, in git if Argo manages them.
- Stargate adds its gateway behaviour as objects it owns: EnvoyProxy access logging and `filterOrder` through the overlay, an EnvoyExtensionPolicy for Warden, and a BackendTrafficPolicy `requestBuffer`.
- The routing reconciler applies only Stargate-owned AIGatewayRoutes (aliases, weights) and never touches `<m>-external` / `<m>-internal`. That settles the open "target ownership model" question from the routing discussion: we never write their routes, only our own.

## Decisions for you

1. **Whose keys guard the external endpoint?** The pack's route-level SecurityPolicy already holds `apiKeyAuth` for each route. Envoy Gateway resolves one SecurityPolicy per target, so a Stargate ext_authz policy on the same routes conflicts with it. I still need to check whether Envoy Gateway 1.8 can merge a gateway-level policy with a route-level one. The options:
   - **A.** Stargate replaces their external auth, either by upstreaming an operator option to skip `apiKeyAuth` or by disabling `endpoints.external` and serving our own routes. Our key model wins, and their key-manager and frontend become unused for external traffic.
   - **B.** Keep their keys, and Stargate maps `x-llm-client-id` to user and model through the metadata ConfigMap. This leaves no project scoping and no rotation; budgets can only be per user.
   - **C.** Upstream a change to the pack so its key-manager stores keys in Stargate.

   I lean towards A. B gives up most of what the Keys page does today.
2. **Upstream or wrap?** Several gaps are cleaner fixed in the pack: an option to turn off `apiKeyAuth`, ownerRefs on `LLMModel` routes, server-side apply with a field manager, and access-log config in the chart. Should we propose these upstream, or work around them on our side for now?
3. **Their frontend.** It overlaps only with our Keys page and uses the same Nebari theme and stack. Should Stargate's console replace it, or link to it?
