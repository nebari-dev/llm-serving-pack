# openmeter-pack

A Nebari software pack that stores the LLM serving pack's usage in
[OpenMeter](https://openmeter.io) and shows it in a read-only console: every
model request's tokens and cost, by team, person, API key and model.

```
gateway access log --> usage collector (LLM serving pack) --> OpenMeter (this pack) --> console
```

The chart (`chart/`, `nebari-openmeter`) deploys:

- **OpenMeter** v1.0.0-beta.234: the API, which takes usage events and
  answers queries, and the sink worker, which moves events from Kafka into
  ClickHouse.
- **Its stores**: Kafka, ClickHouse and Postgres on persistent volumes, and
  Redis for de-duplicating events. All are pinned upstream images.
- **The meters** the collector feeds (`llm_requests`, `llm_tokens`), a
  priced feature per model resource, manual prices for models no public price
  list has, and OpenMeter's public price sync after install and daily.
- **The console**: Stargate's console (`ui/console`) on `openmeter-api`
  (`ui/server/cmd/openmeter-api`), which answers the console's read API from
  OpenMeter. It keeps no data of its own. Optionally it runs behind a
  NebariApp with Keycloak sign-in.

## Install

```bash
helm install usage ./chart -n openmeter --create-namespace \
  --set console.image.tag=<tag> \
  --set nebariapp.enabled=true --set nebariapp.hostname=usage.<your-domain>
```

Then point the LLM serving pack at it:

```yaml
usage:
  enabled: true
  openmeter:
    url: http://usage-openmeter-api.openmeter.svc
```

and list the serving pack's namespace in `networkPolicy.apiClientNamespaces`.
OpenMeter's API has no authentication, so only those namespaces, the console
and the bootstrap job can reach it.

`modelResources` lists the serving pack's model resources and the OpenMeter
provider that prices each one's models (for example `bedrock`). `prices`
sets per-token USD rates for models with no public price.

## What the console shows

Overview, Traffic (one receipt per request, live), Spend (by team, person,
project, key, model or provider, with the month-end projection) and Keys
(each key's owner, team and last 24 hours). It has no pages for things this
pack doesn't do (routing, guardrails, the model catalog, settings). Everyone
who reaches the console is a viewer: there is nothing to edit.

## Known limits

- **Costs use today's prices.** OpenMeter prices all usage with the price in
  effect now, and replacing a manual price deletes the old one. A price
  change therefore changes past costs too. Receipts in the console are priced
  the same way, so they add up to the Spend totals.
- **Times are as fine as OpenMeter's windows.** A key's "last used" is the
  start of the hour (or day) of its last request.
- **No caps.** Neither OpenMeter nor the console stops spend. Caps belong in
  the gateway.

## Develop

```bash
cd ui/server && go test ./internal/openmeter/   # the console API against a fake OpenMeter
cd ui/console && npm ci --ignore-scripts && npx vitest run
dev/kind-e2e.sh   # on the LLM serving pack's kind cluster: install, send traffic, check totals
```

## History

`ui/` started as the Stargate gateway console. Its original commits, with
their authors, are kept unchanged at the start of this repository's history;
the commits after them adapt it to OpenMeter.
