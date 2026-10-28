#!/usr/bin/env bash
# Usage metering end to end on the local kind cluster, with the bundled
# OpenMeter and usage console (the chart's openmeter subchart):
#
#   alice, bob --API key or Keycloak token--> gateway --> mock provider
#                                               |
#                        access log (OTLP) -----+
#                                               v
#                 usage collector --who owns this key?--> key-manager
#                        | usage events
#                        v
#                    OpenMeter <--meter and cost queries-- this script
#                        ^
#                        +--reads-- usage console <--its API-- this script
#
# Two people in the dev Keycloak, alice (team-a) and bob (team-b), mint API
# keys and call the mock provider with keys and with their logins; the totals
# are then read back from OpenMeter's own API (its meters), per person and per
# team, and must cover every request each of them sent. The usage console's
# API must show the same requests, and alice and bob in its spend.
#
# Prereq: `make setup`. Safe to re-run: each run adds traffic and new keys.
# Needs about 3 GB of free memory in the Docker VM (Kafka and ClickHouse).
# OpenMeter's Postgres is a CloudNativePG Cluster (`make deploy-cnpg`).
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
NS=llm-operator-system
GW_NS=envoy-gateway-system
KC_URL=http://keycloak.keycloak.svc.cluster.local:8080
# The openmeter subchart, installed on its own here because the dev cluster
# runs the pack from manifests; in a chart install it is openmeter.enabled.
# Like a chart install, it runs in its own namespace, away from the collector
# and key-manager, with the chart's policy admitting them across namespaces.
OM_RELEASE=usage
OM_NS="${OM_NAMESPACE:-usage-openmeter}"
OM_CHART=../charts/nebari-llm-serving/charts/nebari-openmeter
OPENMETER_URL="http://${OM_RELEASE}-openmeter-api.${OM_NS}.svc"

# Pinned to the kind cluster's context so this never acts on another cluster.
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"
HELM="helm --kube-context kind-${CLUSTER_NAME}"

step() { printf '\n==> %s\n' "$*"; }
retry() { # retry N CMD...: run CMD until it succeeds, N attempts 2s apart
  local n="$1"; shift
  for _ in $(seq 1 "$n"); do "$@" && return 0; sleep 2; done
  "$@"
}

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  echo "ERROR: kind cluster '$CLUSTER_NAME' not found. Run 'make setup' first." >&2
  exit 1
fi

# --- 1. pack, dev Keycloak and its two people -----------------------------------------
step "building and loading images, deploying the operator, key-manager and Keycloak"
make build-images >/dev/null
docker build -q -f ../openmeter-setup/Dockerfile -t openmeter-setup:dev .. >/dev/null
docker build -q -f ../usage-console/Dockerfile -t usage-console:dev .. >/dev/null
make load-images >/dev/null
kind load docker-image openmeter-setup:dev --name "$CLUSTER_NAME" >/dev/null
kind load docker-image usage-console:dev --name "$CLUSTER_NAME" >/dev/null
make deploy >/dev/null
make deploy-keycloak >/dev/null
make deploy-cnpg >/dev/null
# Internal endpoints validate tokens from the in-cluster dev Keycloak. Routes
# record token usage only with metering on, as a chart install with usage.enabled.
$KUBECTL -n "$NS" set env deploy/llm-operator LLM_OIDC_ISSUER_URL="$KC_URL/realms/nebari" \
  LLM_USAGE_METERING_ENABLED=true >/dev/null

step "making sure alice (team-a) and bob (team-b) exist in the dev realm"
./demo-users.sh

# --- 2. OpenMeter and the usage console (the openmeter subchart) ------------------------
if [[ "$OM_NS" != "$NS" ]] && $HELM -n "$NS" status "$OM_RELEASE" >/dev/null 2>&1; then
  # Earlier versions of this script installed it next to the pack; two copies
  # would not fit in the Docker VM. Its volumes are kept.
  step "removing the openmeter subchart left in $NS by an earlier run (now in $OM_NS)"
  $HELM -n "$NS" uninstall "$OM_RELEASE" --wait --timeout 5m
fi
step "installing the openmeter subchart as $OM_RELEASE in $OM_NS; its logs: $KUBECTL -n $OM_NS logs -f deploy/${OM_RELEASE}-openmeter-api"
$HELM upgrade --install "$OM_RELEASE" "$OM_CHART" -n "$OM_NS" --create-namespace \
  -f manifests/usage-openmeter-values.yaml --wait --timeout 10m
# The admission a chart install (openmeter.enabled) renders for the collector
# and key-manager in the operator namespace, from the chart's own template.
helm template "$OM_RELEASE" ../charts/nebari-llm-serving -n "$OM_NS" -s templates/openmeter-networkpolicy.yaml \
  --set fullnameOverride=llm-operator --set platform.baseDomain=local \
  --set auth.oidc.issuerURL=unused --set keyManager.nebariApp.enabled=false --set frontend.enabled=false \
  --set usage.enabled=true --set openmeter.enabled=true --set openmeter.namespace="$OM_NS" --set openmeter.setup.image.tag=dev --set openmeter.console.image.tag=dev \
  | $KUBECTL apply -f - >/dev/null
# A rebuilt image under the same tag needs new pods.
$KUBECTL -n "$OM_NS" rollout restart deploy/${OM_RELEASE}-openmeter-console >/dev/null
$KUBECTL -n "$OM_NS" rollout status deploy/${OM_RELEASE}-openmeter-console --timeout=120s >/dev/null
$KUBECTL -n "$OM_NS" logs job/${OM_RELEASE}-openmeter-bootstrap --tail=1
$KUBECTL -n "$OM_NS" logs deploy/${OM_RELEASE}-openmeter-sink-worker -c sink-start-position --tail=1

# --- 3. key-manager (key owners, customer sync) and the usage collector -------------------
step "pointing the key-manager at $OPENMETER_URL and deploying the usage collector from the chart"
$KUBECTL -n "$NS" patch deploy/llm-key-manager --patch-file manifests/usage-key-manager-patch.yaml >/dev/null
$KUBECTL -n "$NS" set env deploy/llm-key-manager LLM_OPENMETER_URL="$OPENMETER_URL" >/dev/null
$KUBECTL -n "$NS" rollout restart deploy/llm-operator deploy/llm-key-manager >/dev/null
$KUBECTL -n "$NS" rollout status deploy/llm-operator --timeout=120s >/dev/null
$KUBECTL -n "$NS" rollout status deploy/llm-key-manager --timeout=180s >/dev/null
$KUBECTL apply -f manifests/usage-key-owners-service.yaml >/dev/null
helm template dev ../charts/nebari-llm-serving -n "$NS" -s templates/usage-collector.yaml \
  --set fullnameOverride=llm-operator --set platform.baseDomain=local \
  --set auth.oidc.issuerURL=unused --set keyManager.nebariApp.enabled=false --set frontend.enabled=false \
  --set usage.enabled=true --set usage.openmeter.url="$OPENMETER_URL" \
  --set usage.collector.networkPolicy.enabled=false \
  | $KUBECTL apply -f - >/dev/null
# The dev key-manager comes from manifests, not the chart; point at its lookup.
$KUBECTL -n "$NS" set env deploy/llm-operator-usage-collector \
  KEY_OWNER_URL="http://llm-key-manager-owners.${NS}.svc:8081" >/dev/null
$KUBECTL -n "$NS" rollout status deploy/llm-operator-usage-collector --timeout=120s >/dev/null

# --- 4. mock provider behind a PassthroughModel ---------------------------------------------
step "deploying the mock provider and its PassthroughModel"
if ! $KUBECTL -n "$NS" get secret mock-provider-api-key >/dev/null 2>&1; then
  # The gateway sends this upstream; the mock does not check it.
  openssl rand -hex 24 | tr -d '\n' | $KUBECTL -n "$NS" create secret generic mock-provider-api-key \
    --from-file=apiKey=/dev/stdin >/dev/null
fi
# The operator's webhook may not be serving the instant its rollout returns.
retry 30 $KUBECTL apply -f manifests/usage-mock-provider.yaml >/dev/null
$KUBECTL -n "$NS" wait certificate/mock-provider-tls --for=condition=Ready --timeout=120s >/dev/null
$KUBECTL -n "$NS" rollout restart deploy/mock-provider >/dev/null
$KUBECTL -n "$NS" rollout status deploy/mock-provider --timeout=120s >/dev/null
$KUBECTL -n "$NS" wait passthroughmodel/mock-provider --for=jsonpath='{.status.phase}'=Ready --timeout=180s >/dev/null

step "checking that an unlabelled pod in $NS cannot reach OpenMeter's API"
# The mock provider has no openmeter-client label: the policies must drop it.
if $KUBECTL -n "$NS" exec deploy/mock-provider -- python -c \
  'import sys, urllib.request; urllib.request.urlopen(sys.argv[1] + "/api/v1/meters", timeout=5)' "$OPENMETER_URL" >/dev/null 2>&1; then
  echo "ERROR: the mock provider reached $OPENMETER_URL; OpenMeter's API is not restricted" >&2; exit 1
fi

# --- 5. gateway: the published access-log settings ---------------------------------------
step "configuring the gateway with examples/usage-access-log.yaml"
# Public certificate only; the private key stays in the mock's namespace.
$KUBECTL -n "$NS" get secret mock-provider-tls -o jsonpath='{.data.tls\.crt}' | base64 -d \
  | $KUBECTL -n "$GW_NS" create configmap dev-provider-ca --from-file=ca.crt=/dev/stdin \
      --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
$KUBECTL apply -f ../examples/usage-access-log.yaml >/dev/null
$KUBECTL -n "$GW_NS" patch envoyproxy nebari-gateway-proxy --type merge --patch-file manifests/usage-envoyproxy-dev.yaml >/dev/null
# The example names the chart's default release; point it at this cluster's collector.
$KUBECTL -n "$GW_NS" patch envoyproxy nebari-gateway-proxy --type json -p \
  '[{"op":"replace","path":"/spec/telemetry/accessLog/settings/0/sinks/0/openTelemetry/backendRefs/0","value":{"name":"llm-operator-usage-collector","namespace":"llm-operator-system","port":4317}}]' >/dev/null
$KUBECTL patch gatewayclass nebari-gateway-class --type merge -p \
  '{"spec":{"parametersRef":{"group":"gateway.envoyproxy.io","kind":"EnvoyProxy","name":"nebari-gateway-proxy","namespace":"envoy-gateway-system"}}}' >/dev/null
# Envoy Gateway rolls the gateway pods itself after an EnvoyProxy change; wait
# until they stop changing so traffic doesn't land on a pod about to go.
settle_gateways() {
  local last="" now stable=0
  for _ in $(seq 1 120); do
    now="$($KUBECTL -n "$GW_NS" get pods -l gateway.envoyproxy.io/owning-gateway-namespace="$GW_NS" \
      -o jsonpath='{range .items[*]}{.metadata.name}={.status.phase},{end}')"
    if [[ "$now" == "$last" && "$now" != *Pending* && -n "$now" ]]; then
      stable=$((stable + 1)); [[ $stable -ge 8 ]] && return 0
    else
      stable=0
    fi
    last="$now"; sleep 2
  done
}
settle_gateways

# --- 6. port-forwards ------------------------------------------------------------------------
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT INT TERM
gateway_svc() { $KUBECTL -n "$GW_NS" get svc -l gateway.envoyproxy.io/owning-gateway-name="$1" -o name | head -1; }
forward_all() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  PIDS=()
  $KUBECTL -n "$NS" port-forward svc/llm-key-manager 18080:8080 >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$OM_NS" port-forward svc/${OM_RELEASE}-openmeter-api 18893:80 >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$OM_NS" port-forward svc/${OM_RELEASE}-openmeter-console 18892:80 >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$GW_NS" port-forward "$(gateway_svc nebari-gateway)" 18443:443 >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$GW_NS" port-forward "$(gateway_svc nebari-internal-gateway)" 19443:443 >/dev/null 2>&1 & PIDS+=($!)
  sleep 2
}
forward_all
KM=http://localhost:18080
OM=http://localhost:18893
CONSOLE=http://localhost:18892
API="$CONSOLE/api/v1/demo"
retry 30 curl -sf -o /dev/null "$KM/healthz"
retry 30 curl -sf -o /dev/null "$OM/api/v1/meters"
retry 30 curl -sf -o /dev/null "$CONSOLE/healthz"
# Totals over the last 30 days from OpenMeter's meters, as JSON:
# {"all": n, "user": {"alice": n, ...}, "team": {"team-a": n, ...}}.
FROM="$(python3 -c 'import datetime as d; print((d.datetime.now(d.timezone.utc) - d.timedelta(days=30)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
totals() { # meter slug
  local q="$OM/api/v1/meters/$1/query?from=$FROM"
  python3 - "$(curl -sf "$q")" "$(curl -sf "$q&groupBy=user")" "$(curl -sf "$q&groupBy=team")" <<'PY'
import json, sys
def rows(s): return json.loads(s)["data"]
out = {"all": sum(r["value"] for r in rows(sys.argv[1]))}
for i, by in ((2, "user"), (3, "team")):
    out[by] = {}
    for r in rows(sys.argv[i]):
        k = r["groupBy"].get(by) or "(none)"
        out[by][k] = out[by].get(k, 0) + r["value"]
print(json.dumps(out))
PY
}
requests() { totals llm_requests | python3 -c 'import sys, json; print(int(json.load(sys.stdin)["all"]))'; }
before="$(retry 15 totals llm_requests)"
# The console's own count of the last 30 days' requests.
console_requests() { curl -sf "$API/summary?range=30d" | python3 -c 'import sys, json; print(json.load(sys.stdin)["current"]["requests"])'; }
console_before="$(retry 15 console_requests)"

# --- 7. alice and bob: keys and traffic -------------------------------------------------------
token_for() { # user password -> access token (minted inside the cluster: in-cluster issuer)
  $KUBECTL -n "$NS" exec -i deploy/mock-provider -- python -c '
import json, sys, urllib.request, urllib.parse
user, pw = sys.stdin.read().split("\n")[:2]
data = urllib.parse.urlencode({"client_id": "nebari-frontend-spa", "username": user, "password": pw, "grant_type": "password"}).encode()
print(json.load(urllib.request.urlopen("http://keycloak.keycloak.svc.cluster.local:8080/realms/nebari/protocol/openid-connect/token", data))["access_token"])' <<<"$1
$2"
}
mint() { # token description -> api key
  retry 45 curl -sf -X POST "$KM/api/keys" -H "Authorization: Bearer $1" -H 'Content-Type: application/json' \
    -d "{\"modelName\":\"mock-provider\",\"description\":\"$2\"}" | python3 -c 'import sys, json; print(json.load(sys.stdin)["apiKey"])'
}
send() { # host port bearer curl-args... ; retried on HTTP 000 (a dropped port-forward)
  local host="$1" port="$2" bearer="$3" code=000; shift 3
  for _ in 1 2 3 4 5; do
    code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 20 --resolve "$host:$port:127.0.0.1" \
      -H "Authorization: Bearer $bearer" -H 'Content-Type: application/json' "$@" "https://$host:$port/v1/chat/completions" || true)"
    [[ "$code" != "000" ]] && break
    forward_all
  done
  echo "$code"
}
ext() { local key="$1"; shift; send llm.local 18443 "$key" "$@"; }
int() { local tok="$1"; shift; send llm-internal.local 19443 "$tok" "$@"; }
chat() { printf '{"model":"%s","messages":[{"role":"user","content":"%s"}]%s}' "$1" "$2" "${3:-}"; }
SENT=0; FAILED=0
# Requests sent per person (their keys and their logins); every person here
# is in one team (alice team-a, bob team-b).
ALICE_SENT=0; BOB_SENT=0
sent_by() { case "$1" in alice) ALICE_SENT=$((ALICE_SENT + 1)) ;; bob) BOB_SENT=$((BOB_SENT + 1)) ;; esac; }
call() { local who="$1" code; shift; code="$("$@")"; SENT=$((SENT + 1)); sent_by "$who"; [[ "$code" == "200" ]] || FAILED=$((FAILED + 1)); }

step "alice (team-a) and bob (team-b) sign in and mint API keys"
userpw() { $KUBECTL -n keycloak get secret demo-users -o jsonpath="{.data.$1}" | base64 -d; }
ALICE_TOKEN="$(token_for alice "$(userpw alice)")"
BOB_TOKEN="$(token_for bob "$(userpw bob)")"
ALICE_KEY_1="$(mint "$ALICE_TOKEN" "alice notebook")"
ALICE_KEY_2="$(mint "$ALICE_TOKEN" "alice batch job")"
BOB_KEY_1="$(mint "$BOB_TOKEN" "bob app")"

step "waiting for the gateway to accept the new keys"
for who_key in "alice $ALICE_KEY_1" "alice $ALICE_KEY_2" "bob $BOB_KEY_1"; do
  who="${who_key%% *}"; key="${who_key#* }"
  for attempt in $(seq 1 120); do
    code="$(ext "$key" -d "$(chat mock/small "hello")")"
    [[ "$code" == "200" ]] && { SENT=$((SENT + 1)); sent_by "$who"; break; }
    [[ $attempt -eq 120 ]] && { echo "ERROR: gateway never accepted a key (last: HTTP $code)" >&2; exit 1; }
    sleep 2
  done
done
for who in alice bob; do
  tok="$ALICE_TOKEN"; [[ "$who" == bob ]] && tok="$BOB_TOKEN"
  for attempt in $(seq 1 60); do
    [[ "$(int "$tok" -d "$(chat mock/small "hello")")" == "200" ]] && { SENT=$((SENT + 1)); sent_by "$who"; break; }
    [[ $attempt -eq 60 ]] && { echo "ERROR: the internal endpoint never accepted a login" >&2; exit 1; }
    sleep 2
  done
done

step "sending traffic"
for i in 1 2 3; do call alice ext "$ALICE_KEY_1" -d "$(chat mock/large "Draft a project update for the team, part $i, with three bullet points.")"; done
for i in 1 2; do call alice ext "$ALICE_KEY_2" -d "$(chat mock/small "Summarise this batch record number $i.")"; done
call alice ext "$ALICE_KEY_2" -d "$(chat mock/large "Stream a longer answer about ledgers." ',"stream":true')"
call alice int "$ALICE_TOKEN" -d "$(chat mock/large "A question from a notebook on the internal endpoint.")"
ext "$ALICE_KEY_1" --max-time 1 -d "$(chat mock/large "This stream is cancelled by the client." ',"stream":true')" >/dev/null; SENT=$((SENT + 1)); sent_by alice
for i in 1 2 3 4; do call bob ext "$BOB_KEY_1" -d "$(chat mock/small "Classify support ticket $i.")"; done
call bob int "$BOB_TOKEN" -d "$(chat mock/large "Bob asks a longer question on the internal endpoint.")"
echo "  $SENT requests sent (one a cancelled stream), $FAILED of the rest not answered with 200"
[[ $FAILED -eq 0 ]] || { echo "ERROR: $FAILED requests failed" >&2; exit 1; }

# --- 8. the totals, from OpenMeter's meters -------------------------------------------------
step "waiting for the usage to reach OpenMeter (collector, OpenMeter's Kafka and sink, ClickHouse)"
before_all="$(python3 -c 'import sys, json; print(int(json.loads(sys.argv[1])["all"]))' "$before")"
want=$((before_all + SENT))
got="$before_all"
for attempt in $(seq 1 90); do
  got="$(requests || echo "$got")"
  [[ "$got" -ge "$want" ]] && break
  [[ $attempt -eq 90 ]] && { echo "ERROR: OpenMeter counts $got requests, expected $want. Sink: $KUBECTL -n $OM_NS logs deploy/${OM_RELEASE}-openmeter-sink-worker" >&2; exit 1; }
  sleep 2
done
echo "  $got of $want requests"
# A request's event can land before its token events: wait until the token
# total holds still for three reads.
tokens() { totals llm_tokens | python3 -c 'import sys, json; print(int(json.load(sys.stdin)["all"]))'; }
last=""; same=0
for _ in $(seq 1 60); do
  now="$(tokens || echo "")"
  if [[ -n "$now" && "$now" == "$last" ]]; then same=$((same + 1)); [[ $same -ge 2 ]] && break; else same=0; fi
  last="$now"; sleep 2
done

step "checking every person's and every team's requests reached OpenMeter"
# Each person and team must have gained at least what this run sent for them
# (a gateway retry or a rejected warm-up can add more, never less).
python3 - "$before" "$(totals llm_requests)" "$(totals llm_tokens)" "$ALICE_SENT" "$BOB_SENT" <<'PY'
import json, sys
before, after, tokens = (json.loads(a) for a in sys.argv[1:4])
sent = {"alice": int(sys.argv[4]), "bob": int(sys.argv[5])}
teams = {"alice": "team-a", "bob": "team-b"}
bad = []
for by, key in (("user", lambda p: p), ("team", lambda p: teams[p])):
    for person, n in sent.items():
        k = key(person)
        gained = after[by].get(k, 0) - before[by].get(k, 0)
        print("  %-5s %-7s +%3d requests (sent %d), %6d tokens in 30 days" % (by, k, gained, n, tokens[by].get(k, 0)))
        if gained < n:
            bad.append(f"{by} {k}: +{gained} requests, sent {n}")
        if tokens[by].get(k, 0) <= 0:
            bad.append(f"{by} {k}: no tokens")
if bad:
    sys.exit("ERROR: " + "; ".join(bad))
PY

step "checking the key-manager's customer sync reached OpenMeter in $OM_NS"
customers() { curl -sf "$OM/api/v1/customers?pageSize=100" | python3 -c '
import sys, json
names = {c.get("name") for c in json.load(sys.stdin)["items"]}
sys.exit(0 if {"alice", "bob"} <= names else 1)'; }
retry 30 customers || { echo "ERROR: no OpenMeter customers for alice and bob. Key-manager: $KUBECTL -n $NS logs deploy/llm-key-manager" >&2; exit 1; }
echo "  alice and bob are OpenMeter customers"

step "checking the usage console's API shows the same usage"
want=$((console_before + SENT))
got="$console_before"
for attempt in $(seq 1 30); do
  got="$(console_requests || echo "$got")"
  [[ "$got" -ge "$want" ]] && break
  [[ $attempt -eq 30 ]] && { echo "ERROR: the console shows $got requests, expected $want. Console: $KUBECTL -n $OM_NS logs deploy/${OM_RELEASE}-openmeter-console" >&2; exit 1; }
  sleep 2
done
echo "  $got of $want requests in the console's summary"
curl -sf "$API/session" | python3 -c '
import sys, json
s = json.load(sys.stdin)
a = s["actor"]
print("  session: %s, roles %s, sign-in mode %s" % (a.get("name") or a.get("email") or "(none)", ",".join(a.get("roles") or []), s["auth"]["mode"]))'
for by in person team; do
  curl -sf "$API/spend?range=30d&by=$by" | python3 -c '
import sys, json
by, rows = sys.argv[1], json.load(sys.stdin)["rows"]
for r in rows: print("  %-6s %-10s %3d requests %6d tokens  $%.4f" % (by, r["label"], r["requests"], r["tokens"], r["spendUsd"]))
want = {"person": {"alice", "bob"}, "team": {"team-a", "team-b"}}[by]
seen = {r["label"] for r in rows if r["requests"] > 0 and r["tokens"] > 0}
if not want <= seen:
    sys.exit("ERROR: the console has no requests or tokens for " + ", ".join(sorted(want - seen)) + " by " + by)' "$by"
done

step "spend by person and by team, from OpenMeter's cost queries (last 30 days)"
TO="$(python3 -c 'import datetime as d; print(d.datetime.now(d.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
features="$(curl -sf "$OM/api/v3/openmeter/features?page%5Bsize%5D=100")"
for by in user team; do
  for id in $(python3 -c 'import sys, json; print(" ".join(f["id"] for f in json.loads(sys.argv[1])["data"]))' "$features"); do
    curl -sf -X POST "$OM/api/v3/openmeter/features/$id/cost/query" -H 'Content-Type: application/json' \
      -d "{\"from\":\"$FROM\",\"to\":\"$TO\",\"group_by_dimensions\":[\"$by\"]}"
    echo
  done | python3 -c '
import sys, json
by, total = sys.argv[1], {}
for line in sys.stdin:
    for r in json.loads(line)["data"] if line.strip() else []:
        k = r["dimensions"].get(by) or "(none)"
        t = total.setdefault(k, [0.0, 0.0])
        t[0] += float(r["usage"]); t[1] += float(r.get("cost") or 0)
for k, (usage, cost) in sorted(total.items()):
    print("  %-5s %-10s %6d tokens  $%.4f" % (by, k, usage, cost))' "$by"
done
echo
echo "OpenMeter's API: $OM (this script's port-forward). To keep it open: $KUBECTL -n $OM_NS port-forward svc/${OM_RELEASE}-openmeter-api 18893:80"
echo "Usage console: $CONSOLE (this script's port-forward). To keep it open: $KUBECTL -n $OM_NS port-forward svc/${OM_RELEASE}-openmeter-console 18892:80"
