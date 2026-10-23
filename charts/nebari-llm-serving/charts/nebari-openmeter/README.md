# openmeter-pack

A Nebari software pack that stores the LLM serving pack's usage in
[OpenMeter](https://openmeter.io): every model request's tokens and cost, by
team, person, API key and model, read from OpenMeter's API.

```
gateway access log --> usage collector (LLM serving pack) --> OpenMeter (this pack)
```

The chart (`nebari-openmeter`) deploys:

- **OpenMeter** v1.0.0-beta.234: the API, which takes usage events and
  answers queries, and the sink worker, which moves events from Kafka into
  ClickHouse.
- **Its stores**: Kafka, ClickHouse and Postgres on persistent volumes, and
  Redis for de-duplicating events. All are pinned upstream images.
  Postgres's credentials come from CloudNativePG. ClickHouse's password is
  `clickhouse.existingSecret`, or a Secret a pre-install/pre-upgrade hook
  (Argo CD `PreSync`) creates once with a random password and never changes,
  so it stays put when Argo CD re-renders the chart.
- **The meters** the collector feeds (`llm_requests`, `llm_tokens`), a
  priced feature per model resource, manual prices for models no public price
  list has, and OpenMeter's public price sync after install and daily.
- **Two setup Jobs** from the `openmeter-setup` image (`openmeter-setup/` in
  this repository): `bootstrap` creates the features and prices after each
  install or upgrade, and `ensure-secret` makes the ClickHouse password once.

## Install

As part of the LLM serving pack, turn it on with `openmeter.enabled: true`
(and `usage.enabled: true`). It then runs in the release namespace, and the
serving pack's chart adds a `NetworkPolicy` that admits the usage collector
and the key-manager, which run in the operator namespace, to OpenMeter's API.

On its own:

```bash
helm install usage . -n openmeter --create-namespace \
  --set setup.image.tag=<tag>
```

Then point the LLM serving pack at it:

```yaml
usage:
  enabled: true
  openmeter:
    url: http://usage-openmeter-api.openmeter.svc
```

and admit the serving pack's clients to the API: a `NetworkPolicy` in this
namespace like the serving pack's `templates/openmeter-networkpolicy.yaml`
(pods labelled `llm.nebari.dev/openmeter-client: "true"` in the operator
namespace), or list the operator namespace in
`networkPolicy.apiClientNamespaces`, which admits every pod in it.
OpenMeter's API has no authentication, so only the admitted pods and the
bootstrap job can reach it.

`modelResources` lists the serving pack's model resources and the OpenMeter
provider that prices each one's models (for example `bedrock`). `prices`
sets per-token USD rates for models with no public price.

## Known limits

- **Costs use today's prices.** OpenMeter prices all usage with the price in
  effect now, and replacing a manual price deletes the old one; it keeps no
  price history. A price change therefore changes past costs too.
- **Repeats are dropped while Redis remembers them.** The sink worker keeps
  each event id in Redis for `openmeter.dedupeExpiration`, in memory only. A
  Redis restart forgets them, and when Redis reaches `redis.maxMemory` it
  evicts the ids closest to expiring; an event redelivered after either is
  counted again.
- **No caps.** OpenMeter does not stop spend. Caps belong in the gateway.

## Develop

```bash
cd openmeter-setup && go test ./...   # bootstrap and ensure-secret against fakes
cd dev && make usage-e2e              # on the kind cluster: install, send traffic, check totals
```
