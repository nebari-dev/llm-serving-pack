# openmeter-pack

A Nebari software pack that stores the LLM serving pack's usage in
[OpenMeter](https://openmeter.io) and shows it in a read-only console: every
model request's tokens and cost, by team, person, API key and model.

```
gateway access log --> usage collector (LLM serving pack) --> OpenMeter (this pack) --> console
```

The chart (`nebari-openmeter`) deploys:

- **OpenMeter** v1.0.0-beta.234: the API, which takes usage events and
  answers queries, and the sink worker, which moves events from Kafka into
  ClickHouse.
- **Its stores**: Kafka, ClickHouse, Redis (for dropping repeated events,
  with an append-only file) and Postgres, on persistent volumes. All are
  pinned upstream images.
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
- **The console**: Stargate's console (`usage-console/console`) on
  `openmeter-api` (`usage-console/server/cmd/openmeter-api`), which answers
  the console's read API from OpenMeter. It keeps no data of its own.
  Optionally it runs behind a NebariApp with Keycloak sign-in.

## Install

As part of the LLM serving pack, turn it on with `openmeter.enabled: true`
(and `usage.enabled: true`). It then runs in the release namespace, and the
serving pack's chart adds a `NetworkPolicy` that admits the usage collector
and the key-manager, which run in the operator namespace, to OpenMeter's API.

On its own:

```bash
helm install usage . -n openmeter --create-namespace \
  --set setup.image.tag=<tag> --set console.image.tag=<tag> \
  --set nebariapp.enabled=true --set nebariapp.hostname=usage.<your-domain>
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
OpenMeter's API has no authentication, so only the admitted pods, the
console and the bootstrap job can reach it.

`modelResources` lists the serving pack's model resources and the OpenMeter
provider that prices each one's models (for example `bedrock`). `prices`
sets per-token USD rates for models with no public price.

## What the console shows

Overview, Traffic (one receipt per request, live), Spend (by team, person,
project, key, model or provider, with the month-end projection) and Keys
(each key's owner, team and last 24 hours). It has no pages for things this
pack doesn't do (routing, guardrails, the model catalog, settings). Everyone
who reaches the console is a viewer: there is nothing to edit.

People sign in with Keycloak at the gateway (the NebariApp's
`auth.enforceAtGateway`). The gateway forwards each person's access token to
the console (`nebariapp.auth.forwardAccessToken`), which checks it against
Keycloak's published keys and shows who is signed in; Sign out goes to the
gateway's `/logout`, which ends the gateway session and Keycloak's. The
console answers 401 to a request without a valid token, so its data can't be
read around the gateway. `nebariapp.auth.groups` limits the console to
members of those Keycloak groups. `console.oidc` sets the issuer and client
by hand; see `values.yaml`.

## Known limits

- **Costs use today's prices.** OpenMeter prices all usage with the price in
  effect now, and replacing a manual price deletes the old one; it keeps no
  price history. A price change therefore changes past costs too. A receipt
  in the console shows its request at today's price, not the price when the
  request ran, so receipts add up to the Spend totals.
- **Repeats are dropped while Redis remembers them, best effort.** The sink
  worker keeps each event id in Redis for `openmeter.dedupeExpiration`.
  Redis logs every write to an append-only file on its volume, synced once a
  second, so a restart keeps the ids, but a crash can lose the last second
  of them (and with `redis.persistence.enabled: false` a restart forgets
  them all). When Redis reaches `redis.maxMemory` it evicts the ids closest
  to expiring. An event redelivered after its id is gone is counted again.
- **Times are as fine as OpenMeter's windows.** A key's "last used" is the
  start of the hour (or day) of its last request.
- **No caps.** Neither OpenMeter nor the console stops spend. Caps belong in
  the gateway.
- **ClickHouse keeps only some of its own logs.** Its trace, text, part,
  metric and query metric logs are off, and its query and error logs expire
  after 7 days, so they can't grow until ClickHouse runs out of memory for
  inserts. Usage events never expire. An install from before this change
  keeps the log tables it already has, and ClickHouse keeps merging them,
  which can still use up its memory and stop usage reaching it. Drop them
  once after upgrading:
  `kubectl -n <namespace> exec <release>-openmeter-clickhouse-0 -- clickhouse-client -q "DROP TABLE IF EXISTS system.trace_log SYNC"`
  (and the same for `text_log`, `part_log`, `metric_log`,
  `asynchronous_metric_log`, `processors_profile_log`, `query_metric_log`,
  `query_log_0` and `error_log_0`). The `_0` tables are the old query and
  error logs, renamed by the upgrade, and never expire. Keep `query_log` and
  `error_log`: those are the new ones that expire.

## Uninstall and reinstall

`keepDataOnUninstall` decides what `helm uninstall`, or deleting the Argo CD
Application, does with the data. Both settings keep the usage and the billing
setup together, so a reinstall never finds usage events without the
customers, features and prices that go with them.

- **`true` (the default): keep everything.** The Postgres `Cluster` carries
  `helm.sh/resource-policy: keep` and `argocd.argoproj.io/sync-options:
  Delete=false`, so it stays, with its volume (customers, features, prices).
  The Kafka, ClickHouse and Redis volumes stay too
  (`persistentVolumeClaimRetentionPolicy.whenDeleted: Retain`). Install again
  with the same release name and namespace and OpenMeter carries on with the
  same usage and billing setup: Helm and Argo CD take the kept `Cluster`
  back, and the StatefulSets mount the kept volumes.
- **`false`: delete everything with the release.** The `Cluster` is deleted
  (CloudNativePG deletes its volume with it), and the StatefulSets' volumes
  are deleted with them (`whenDeleted: Delete`). Set it with a `helm upgrade`
  (or an Argo CD sync) before uninstalling; the setting in effect when the
  release is removed is the one that applies.

Keeping is the default because the usage is what you bill from: losing it
should take a deliberate step. In both cases the ClickHouse password Secret
(`<release>-openmeter-clickhouse-password`, made by a hook, not by the
release) stays. A reinstall reuses it, which is what kept ClickHouse data
needs; a fresh ClickHouse takes it as its new password.

To delete kept data by hand, after `helm uninstall`:

```bash
kubectl -n <namespace> delete cluster <release>-openmeter-db
kubectl -n <namespace> delete pvc -l app.kubernetes.io/name=nebari-openmeter,app.kubernetes.io/instance=<release>
kubectl -n <namespace> delete secret <release>-openmeter-clickhouse-password
```

The store volumes carry the StatefulSets' selector labels, which is what the
second command matches; Postgres's volume goes with its `Cluster`.

## Develop

```bash
cd openmeter-setup && go test ./...   # bootstrap and ensure-secret against fakes
cd usage-console/server && go test ./internal/openmeter/   # the console API against a fake OpenMeter
cd usage-console/console && npm ci --ignore-scripts && npx vitest run
cd dev && make usage-e2e              # on the kind cluster: install, send traffic, check totals
```

## History

The usage console (`usage-console/`) started as the Stargate gateway console.
Its original commits, with their authors, are kept in this repository's
history; the commits after them adapt it to OpenMeter and keep only what this
pack uses.
