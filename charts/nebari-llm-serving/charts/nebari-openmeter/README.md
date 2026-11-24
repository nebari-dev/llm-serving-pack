# openmeter-pack

A Nebari software pack that stores the LLM serving pack's usage in
[OpenMeter](https://openmeter.io) and shows it in a read-only console: every
model request's tokens and cost, by team, person, API key and model.

```
gateway access log --> usage collector (LLM serving pack) --> OpenMeter (this pack) --> console
```

The chart (`nebari-openmeter`) deploys:

- **OpenMeter** v1.0.0-beta.234, built by
  [nebari-dev/openmeter](https://github.com/nebari-dev/openmeter/tree/nebari/v1.0.0-beta.234)
  with app secrets encrypted at rest (see [Stripe](#stripe)): the API, which
  takes usage events and answers queries, and the sink worker, which moves
  events from Kafka into ClickHouse.
- **Its stores**: Kafka, ClickHouse, Redis (for dropping repeated events,
  with an append-only file) and Postgres, on persistent volumes. All are
  pinned upstream images.
  Postgres's credentials come from CloudNativePG. ClickHouse's password is
  `clickhouse.existingSecret`, and the key OpenMeter encrypts app secrets
  with is `openmeter.secretEncryption.existingSecret`; for each one left
  empty, a Job creates a Secret once with a random value and never changes
  it, so it stays put when Argo CD re-renders the chart. The Job is an ordinary resource, not a hook, so the chart can
  install into a namespace created in the same release.
- **The meters** the collector feeds (`llm_requests`, `llm_tokens`), a
  priced feature per model resource, manual prices for models no public price
  list has, and OpenMeter's public price sync after install and daily.
- **Two setup Jobs** from the `openmeter-setup` image (`openmeter-setup/` in
  this repository): `bootstrap` creates the features and prices after each
  install or upgrade, and `ensure-secret` makes the ClickHouse password and the encryption key
  once.
- **The console**: Stargate's console (`usage-console/console`) on
  `openmeter-api` (`usage-console/server/cmd/openmeter-api`), which answers
  the console's read API from OpenMeter. It keeps no data of its own.
  Optionally it runs behind a NebariApp with Keycloak sign-in.

## Install

As part of the LLM serving pack, turn it on with `openmeter.enabled: true`
(and `usage.enabled: true`). It then runs in the pack's namespace
(`global.namespace`), alongside the usage collector and the key-manager, or in
`namespace` when that is set. In a separate namespace, the serving pack's
chart adds a `NetworkPolicy` that admits the collector and the key-manager
across to OpenMeter's API.

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

## Stripe

OpenMeter's Stripe app sends invoices and takes payments through your Stripe
account. Installing it registers a Stripe webhook at
`<openmeter.apps.baseURL>/api/v1/apps/<app id>/stripe/webhook`, so set
`openmeter.apps.baseURL` to the public https base URL Stripe will reach before
you install it: the URL stays fixed for that install, and the default,
`https://example.com`, reaches nothing. This chart doesn't expose OpenMeter's
API; route only `POST` on that one path to the API Service from outside
(OpenMeter's API has no authentication, and the webhook checks Stripe's
signature), and admit the gateway's proxies to the API port in a
`NetworkPolicy`.

OpenMeter keeps the Stripe app's API key and webhook secret in Postgres,
AES-256-GCM encrypted with the key in `openmeter.secretEncryption` (stock
OpenMeter stores them in plaintext). Back that key up with the database: a
restored database can't use its Stripe secrets without it, and the Stripe
app has to be set up again. A Stripe app installed with an image that stored
them in plaintext keeps working, but its secrets stay plaintext until the app
is installed again.

## The OpenMeter fork

The chart runs a fork of OpenMeter because stock OpenMeter stores the Stripe
app's API key and webhook secret in Postgres in plaintext. The patch lives in
[nebari-dev/openmeter](https://github.com/nebari-dev/openmeter), on branch
`nebari/v1.0.0-beta.234`, released as tag `v1.0.0-beta.234-nebari.1`. Its
`.github/workflows/nebari-image.yaml` runs the tests on every push to a
`nebari/**` branch, and on a `v*-nebari.*` tag also publishes a multi-arch
(amd64 and arm64) image to `ghcr.io/nebari-dev/openmeter` and
`quay.io/nebari/openmeter`.

To move to a new OpenMeter release:

1. Create `nebari/<upstream tag>` from the upstream tag, and cherry-pick the
   fork's commits (those on the current `nebari/` branch after its upstream
   tag) onto it.
2. Push the branch, and check that the tests pass.
3. Push tag `<upstream tag>-nebari.<n>` (`n` starting at 1) to publish the
   image.
4. Pin the new image by digest in `openmeter.image` in `values.yaml`, and
   bump `appVersion` in `Chart.yaml`.
5. Move the usage collector's `benthos-collector` image to the same OpenMeter
   version: `usage.collector.image` in `charts/nebari-llm-serving/values.yaml`
   and `USAGE_COLLECTOR_IMAGE` in `dev/Makefile`.

Drop the fork, and go back to `ghcr.io/openmeterio/openmeter`, once upstream
OpenMeter encrypts app secrets itself.

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
by hand; see `values.yaml`. The chart won't render the console with no
sign-in unless `console.devMode` is set (dev clusters only), and
`console.enabled: false` leaves it out.

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
- **Usage is stored in batches.** The sink worker commits events to
  ClickHouse when `openmeter.sinkWorker.minCommitCount` (500) are waiting or
  `openmeter.sinkWorker.maxCommitWait` (2s) has passed, so an event on an
  idle install shows up to 2 seconds late. Lowering `minCommitCount` to 1
  caps the sink at about one event a second. See the comment on these
  settings in `values.yaml`.
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

It keeps data only in a namespace that outlives the release. As part of the
LLM serving pack, OpenMeter runs in the pack's namespace by default, and if
the pack's chart created that namespace (`createNamespace: true`),
uninstalling the pack deletes it with everything in it, kept data included.
Put OpenMeter in a namespace you created (`openmeter.namespace`), or let
something else create the pack's namespace (`createNamespace: false`; see
`examples/argocd-application-openmeter.yaml`).

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
(`<release>-openmeter-clickhouse-password`, made by a Job, not by the
release) stays, unless its namespace is deleted with the release. A reinstall
reuses it, which is what kept ClickHouse data
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
