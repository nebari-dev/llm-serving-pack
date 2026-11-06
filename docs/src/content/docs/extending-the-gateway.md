---
title: Extending the gateway
description: Behaviors of the pack's gateway that other deployments may build on.
---

You can attach your own Envoy Gateway policies to the pack's model routes
without changing the pack: an external processor that enforces usage limits,
for example. This page lists what the pack does that you may rely on when you
do, and what to watch for.

:::note[Stability]
These behaviors are not frozen. The pack's CRDs are `v1alpha1`. A release that
changes one of them names its ID (C1 to C4) in a heading under
[Release-specific steps](/upgrading/#release-specific-steps), says what
changed, and says what a deployment that relies on it must do.
:::

C2 and C3 hold only with usage metering on (`usage.enabled`). See
[Usage Metering](/usage/).

## C1: Model routes carry a selectable label

Every `AIGatewayRoute` the operator creates, for `LLMModel` and
`PassthroughModel` alike, carries the label
`app.kubernetes.io/managed-by: nebari-llm-operator`. Envoy AI Gateway copies
an `AIGatewayRoute`'s labels onto the `HTTPRoute` it generates, and policies
attach to that `HTTPRoute`. So a policy can select every model route, including
models created later, with:

```yaml
targetSelectors:
  - group: gateway.networking.k8s.io
    kind: HTTPRoute
    matchLabels:
      app.kubernetes.io/managed-by: nebari-llm-operator
```

Selectors match only in the policy's own namespace. The pack's admission
webhooks keep every model in the pack's namespace, so one policy there covers
them all.

## C2: Caller identity headers can be trusted after authentication

Each shared listener removes these headers from the caller's request before
any filter runs:

- `x-llm-client-id`
- `x-auth-user`
- `x-auth-subject`
- `x-auth-groups`

Authentication then sets them: `x-llm-client-id` from the verified API key on
the external endpoint, and the `x-auth-*` headers from the verified token on
the internal endpoint. A filter that runs after authentication may trust them.

## C3: Usage events carry the request ID

Each `llm.request` event the pack sends to OpenMeter has the request's
`x-request-id` as its CloudEvent `id`. The token events for the same request
(`llm.tokens`) use `<request-id>:<token-type>`. Envoy generates the request
ID on each shared listener and replaces any the caller sent, so a filter and
the usage events see the same, trustworthy ID.

## C4: Do not add a ClientTrafficPolicy to the pack's listeners

C2 and C3 come from one `ClientTrafficPolicy` per shared listener, which the
operator manages. Envoy Gateway applies only one `ClientTrafficPolicy` to a
listener, and a policy that targets the listener directly replaces a
Gateway-wide one rather than merging with it.

- **A policy that targets one of the pack's listeners** (with `sectionName`)
  can't share it. The operator removes its own policy and posts a Warning
  event on the Gateway, and C2 and C3 stop holding on that listener: callers
  can then set the identity headers and the request ID themselves.
- **A Gateway-wide policy** is safe. The operator copies its settings into its
  own per-listener policy, so they still apply.

Put any connection or header settings you need on a Gateway-wide policy.

## Notes for external processors

An `EnvoyExtensionPolicy` that adds an external processor to the model routes
runs inside Envoy Gateway's filter chain. These follow from where Envoy
Gateway and Envoy AI Gateway put their filters, not from the pack, so check
them again when you upgrade either:

- **It runs after authentication and before authorization.** It sees requests
  that authorization then denies with 403, such as a valid API key used on a
  model it isn't allowed to call. Don't commit anything, such as quota, until
  the request has passed authorization. Envoy's `response.code_details` on the
  response tells a 401 or 403 the gateway answered itself apart from one the
  model answered (`via_upstream`).
- **It runs after Envoy AI Gateway's own processor.** On Envoy AI Gateway
  1.1, that processor keeps a copy of the request body, and rebuilds the body
  it sends to the model from that copy whenever it rewrites the body: when it
  translates a request for a provider such as Bedrock, and, with usage metering
  on, for a streaming chat request without `stream_options.include_usage`. An
  edit your processor makes to the body, such as lowering `max_tokens`, does
  not reach the model in those cases.
- **A request it refuses still produces an `llm.request` event**, with its
  status in `response_code` and `usage_reported` set to `"false"`. Only 401s
  and 403s the gateway answered itself are left out. Filter on
  `response_code` if you count requests.
- **With `failOpen: false`, its availability becomes the models'.** If it
  can't answer, every model request fails. Run it with several replicas and a
  PodDisruptionBudget.
- **To read the pack's OpenMeter**, admit your namespace with
  `openmeter.networkPolicy.apiClientNamespaces`. OpenMeter's API has no
  authentication, and that value admits every pod in the namespace, so use a
  namespace that holds only your OpenMeter clients.
