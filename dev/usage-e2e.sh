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
#                    OpenMeter <--reads-- usage console
#
# Two people in the dev Keycloak, alice (team-a) and bob (team-b), mint API
# keys and call the mock provider with keys and with their logins; the totals
# are then read back through the usage console's API and must cover every
# request sent.
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
OM_RELEASE=usage
OM_CHART=../charts/nebari-llm-serving/charts/nebari-openmeter
OPENMETER_URL="http://${OM_RELEASE}-openmeter-api.${NS}.svc"

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
docker build -q -t usage-console:dev ../usage-console >/dev/null
make load-images >/dev/null
kind load docker-image usage-console:dev --name "$CLUSTER_NAME" >/dev/null
make deploy >/dev/null
make deploy-keycloak >/dev/null
make deploy-cnpg >/dev/null
# Internal endpoints validate tokens from the in-cluster dev Keycloak.
$KUBECTL -n "$NS" set env deploy/llm-operator LLM_OIDC_ISSUER_URL="$KC_URL/realms/nebari" >/dev/null

step "making sure alice (team-a) and bob (team-b) exist in the dev realm"
# Random passwords, made once and kept in the Secret keycloak/demo-users.
if ! $KUBECTL -n keycloak get secret demo-users >/dev/null 2>&1; then
  $KUBECTL -n keycloak create secret generic demo-users \
    --from-literal=alice="$(openssl rand -base64 24)" --from-literal=bob="$(openssl rand -base64 24)" >/dev/null
fi
userpw() { $KUBECTL -n keycloak get secret demo-users -o jsonpath="{.data.$1}" | base64 -d; }
admin_pw="$($KUBECTL -n keycloak get deploy keycloak -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="KC_BOOTSTRAP_ADMIN_PASSWORD")].value}')"
# Passwords go over stdin, never on a command line.
{ printf '%s\n%s\n%s\n' "$admin_pw" "$(userpw alice)" "$(userpw bob)"; } | $KUBECTL -n keycloak exec -i deploy/keycloak -- bash -c '
set -e
read -r ADMIN_PW; read -r ALICE_PW; read -r BOB_PW
KC=/opt/keycloak/bin/kcadm.sh
$KC config credentials --server http://localhost:8080 --realm master --user admin --password "$ADMIN_PW" >/dev/null
for g in llm-users team-a team-b; do
  [ -n "$($KC get groups -r nebari -q search=$g --fields name 2>/dev/null | grep "\"$g\"")" ] || $KC create groups -r nebari -s name=$g >/dev/null
done
user() { # name team password
  if [ -z "$($KC get users -r nebari -q username=$1 -q exact=true --fields id | grep id)" ]; then
    $KC create users -r nebari -s username=$1 -s enabled=true -s email=$1@example.com -s emailVerified=true -s firstName=$1 -s lastName=demo >/dev/null
  fi
  $KC set-password -r nebari --username $1 --new-password "$3" >/dev/null
  id=$($KC get users -r nebari -q username=$1 -q exact=true --fields id | sed -n "s/.*\"id\" *: *\"\([^\"]*\)\".*/\1/p")
  for g in llm-users $2; do
    gid=$($KC get groups -r nebari -q search=$g -q exact=true --fields id,name | grep -B1 "\"$g\"" | sed -n "s/.*\"id\" *: *\"\([^\"]*\)\".*/\1/p")
    $KC update users/$id/groups/$gid -r nebari -s realm=nebari -s userId=$id -s groupId=$gid -n >/dev/null
  done
}
user alice team-a "$ALICE_PW"
user bob team-b "$BOB_PW"
'
unset admin_pw

# --- 2. OpenMeter and the usage console (the openmeter subchart) ------------------------
step "installing the openmeter subchart as $OM_RELEASE in $NS; its logs: $KUBECTL -n $NS logs -f deploy/${OM_RELEASE}-openmeter-api"
$HELM upgrade --install "$OM_RELEASE" "$OM_CHART" -n "$NS" -f manifests/usage-openmeter-values.yaml --wait --timeout 10m
# A rebuilt image under the same tag needs new pods.
$KUBECTL -n "$NS" rollout restart deploy/${OM_RELEASE}-openmeter-console >/dev/null
$KUBECTL -n "$NS" rollout status deploy/${OM_RELEASE}-openmeter-console --timeout=120s >/dev/null
$KUBECTL -n "$NS" logs job/${OM_RELEASE}-openmeter-bootstrap --tail=1
$KUBECTL -n "$NS" logs deploy/${OM_RELEASE}-openmeter-sink-worker -c sink-start-position --tail=1

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
  $KUBECTL -n "$NS" port-forward svc/${OM_RELEASE}-openmeter-console 18892:80 >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$GW_NS" port-forward "$(gateway_svc nebari-gateway)" 18443:443 >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$GW_NS" port-forward "$(gateway_svc nebari-internal-gateway)" 19443:443 >/dev/null 2>&1 & PIDS+=($!)
  sleep 2
}
forward_all
KM=http://localhost:18080
CONSOLE=http://localhost:18892
API="$CONSOLE/api/v1/demo"
retry 30 curl -sf -o /dev/null "$KM/healthz"
retry 30 curl -sf -o /dev/null "$CONSOLE/healthz"
requests() { curl -sf "$API/summary?range=30d" | python3 -c 'import sys, json; print(json.load(sys.stdin)["current"]["requests"])'; }
before="$(retry 15 requests)"

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
call() { local code; code="$("$@")"; SENT=$((SENT + 1)); [[ "$code" == "200" ]] || FAILED=$((FAILED + 1)); }

step "alice (team-a) and bob (team-b) sign in and mint API keys"
ALICE_TOKEN="$(token_for alice "$(userpw alice)")"
BOB_TOKEN="$(token_for bob "$(userpw bob)")"
ALICE_KEY_1="$(mint "$ALICE_TOKEN" "alice notebook")"
ALICE_KEY_2="$(mint "$ALICE_TOKEN" "alice batch job")"
BOB_KEY_1="$(mint "$BOB_TOKEN" "bob app")"

step "waiting for the gateway to accept the new keys"
for key in "$ALICE_KEY_1" "$ALICE_KEY_2" "$BOB_KEY_1"; do
  for attempt in $(seq 1 120); do
    code="$(ext "$key" -d "$(chat mock/small "hello")")"
    [[ "$code" == "200" ]] && { SENT=$((SENT + 1)); break; }
    [[ $attempt -eq 120 ]] && { echo "ERROR: gateway never accepted a key (last: HTTP $code)" >&2; exit 1; }
    sleep 2
  done
done
for tok in "$ALICE_TOKEN" "$BOB_TOKEN"; do
  for attempt in $(seq 1 60); do
    [[ "$(int "$tok" -d "$(chat mock/small "hello")")" == "200" ]] && { SENT=$((SENT + 1)); break; }
    [[ $attempt -eq 60 ]] && { echo "ERROR: the internal endpoint never accepted a login" >&2; exit 1; }
    sleep 2
  done
done

step "sending traffic"
for i in 1 2 3; do call ext "$ALICE_KEY_1" -d "$(chat mock/large "Draft a project update for the team, part $i, with three bullet points.")"; done
for i in 1 2; do call ext "$ALICE_KEY_2" -d "$(chat mock/small "Summarise this batch record number $i.")"; done
call ext "$ALICE_KEY_2" -d "$(chat mock/large "Stream a longer answer about ledgers." ',"stream":true')"
call int "$ALICE_TOKEN" -d "$(chat mock/large "A question from a notebook on the internal endpoint.")"
ext "$ALICE_KEY_1" --max-time 1 -d "$(chat mock/large "This stream is cancelled by the client." ',"stream":true')" >/dev/null; SENT=$((SENT + 1))
for i in 1 2 3 4; do call ext "$BOB_KEY_1" -d "$(chat mock/small "Classify support ticket $i.")"; done
call int "$BOB_TOKEN" -d "$(chat mock/large "Bob asks a longer question on the internal endpoint.")"
echo "  $SENT requests sent (one a cancelled stream), $FAILED of the rest not answered with 200"
[[ $FAILED -eq 0 ]] || { echo "ERROR: $FAILED requests failed" >&2; exit 1; }

# --- 8. the totals, through the usage console ---------------------------------------------------
step "waiting for the usage to reach the console (collector, OpenMeter's Kafka and sink, ClickHouse)"
want=$((before + SENT))
got="$before"
for attempt in $(seq 1 90); do
  got="$(requests || echo "$got")"
  [[ "$got" -ge "$want" ]] && break
  [[ $attempt -eq 90 ]] && { echo "ERROR: the console shows $got requests, expected $want. Sink: $KUBECTL -n $NS logs deploy/${OM_RELEASE}-openmeter-sink-worker" >&2; exit 1; }
  sleep 2
done
echo "  $got of $want requests"
# A request's event can land before its token events: wait until the token
# total holds still for three reads.
tokens() { curl -sf "$API/spend?range=30d&by=team" | python3 -c 'import sys, json; print(sum(r["tokens"] for r in json.load(sys.stdin)["rows"]))'; }
last=""; same=0
for _ in $(seq 1 60); do
  now="$(tokens || echo "")"
  if [[ -n "$now" && "$now" == "$last" ]]; then same=$((same + 1)); [[ $same -ge 2 ]] && break; else same=0; fi
  last="$now"; sleep 2
done

step "spend by person and by team, from the usage console's API (last 30 days)"
for by in person team; do
  curl -sf "$API/spend?range=30d&by=$by" | python3 -c '
import sys, json
for r in json.load(sys.stdin)["rows"]: print("  %-10s %3d requests %6d tokens  $%.4f" % (r["label"], r["requests"], r["tokens"], r["spendUsd"]))'
done
echo
echo "Usage console: $CONSOLE (this script's port-forward). To keep it open: $KUBECTL -n $NS port-forward svc/${OM_RELEASE}-openmeter-console 18892:80"
