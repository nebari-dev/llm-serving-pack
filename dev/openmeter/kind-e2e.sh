#!/usr/bin/env bash
# The OpenMeter pack on the LLM serving pack's kind cluster, end to end:
#
#   gateway access log --> usage collector (serving pack) --> OpenMeter (this pack) --> console
#
# Builds the console image, installs this chart, points the serving pack's
# usage collector and key-manager at it, then alice (team-a) and bob
# (team-b) mint API keys and call the mock provider with keys and with their
# logins, and the totals are read back through the console's API.
#
# Prereq: the serving pack's dev cluster with usage metering, from its
# dev/usage-demo.sh (cluster, Keycloak with alice and bob, mock provider,
# usage collector). Safe to re-run: each run adds traffic and new keys.
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"
HELM="helm --kube-context kind-${CLUSTER_NAME}"
OM_NS=openmeter-pack
RELEASE=usage
SERVING_NS=llm-operator-system
GW_NS=envoy-gateway-system
OPENMETER_URL="http://${RELEASE}-openmeter-api.${OM_NS}.svc"

step() { printf '\n==> %s\n' "$*"; }
retry() { local n="$1"; shift; for _ in $(seq 1 "$n"); do "$@" && return 0; sleep 2; done; "$@"; }

step "building the console image and loading it into kind"
docker build -q -t openmeter-console:dev --build-arg VERSION="$(git rev-parse --short HEAD)" ../ui >/dev/null
kind load docker-image openmeter-console:dev --name "$CLUSTER_NAME" >/dev/null

step "installing the chart ($RELEASE in $OM_NS); its logs: $KUBECTL -n $OM_NS logs -f deploy/${RELEASE}-openmeter-api"
$HELM upgrade --install "$RELEASE" ../chart -n "$OM_NS" --create-namespace -f values-kind.yaml --wait --timeout 10m
# A rebuilt image under the same tag needs new pods.
$KUBECTL -n "$OM_NS" rollout restart deploy/${RELEASE}-openmeter-console >/dev/null
$KUBECTL -n "$OM_NS" rollout status deploy/${RELEASE}-openmeter-console --timeout=120s >/dev/null
$KUBECTL -n "$OM_NS" logs job/${RELEASE}-openmeter-bootstrap --tail=3
$KUBECTL -n "$OM_NS" logs deploy/${RELEASE}-openmeter-sink-worker -c sink-start-position --tail=2

step "pointing the serving pack's usage collector and key-manager at $OPENMETER_URL"
$KUBECTL -n "$SERVING_NS" set env deploy/llm-operator-usage-collector OPENMETER_URL="$OPENMETER_URL" >/dev/null
$KUBECTL -n "$SERVING_NS" set env deploy/llm-key-manager LLM_OPENMETER_URL="$OPENMETER_URL" >/dev/null
$KUBECTL -n "$SERVING_NS" rollout status deploy/llm-operator-usage-collector --timeout=120s >/dev/null
$KUBECTL -n "$SERVING_NS" rollout status deploy/llm-key-manager --timeout=180s >/dev/null

PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT INT TERM
gateway_svc() { $KUBECTL -n "$GW_NS" get svc -l gateway.envoyproxy.io/owning-gateway-name="$1" -o name | head -1; }
forward_all() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  PIDS=()
  $KUBECTL -n "$SERVING_NS" port-forward svc/llm-key-manager 18080:8080 >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$OM_NS" port-forward svc/${RELEASE}-openmeter-console 18892:80 >/dev/null 2>&1 & PIDS+=($!)
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
before="$(curl -sf "$API/summary?range=30d" | python3 -c 'import sys, json; print(json.load(sys.stdin)["current"]["requests"])')"

token_for() { # user password -> access token (minted inside the cluster: in-cluster issuer)
  $KUBECTL -n "$SERVING_NS" exec -i deploy/mock-provider -- python -c '
import json, sys, urllib.request, urllib.parse
user, pw = sys.stdin.read().split("\n")[:2]
data = urllib.parse.urlencode({"client_id": "nebari-frontend-spa", "username": user, "password": pw, "grant_type": "password"}).encode()
print(json.load(urllib.request.urlopen("http://keycloak.keycloak.svc.cluster.local:8080/realms/nebari/protocol/openid-connect/token", data))["access_token"])' <<<"$1
$2"
}
userpw() { $KUBECTL -n keycloak get secret demo-users -o jsonpath="{.data.$1}" | base64 -d; }
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
  for attempt in $(seq 1 60); do [[ "$(int "$tok" -d "$(chat mock/small "hello")")" == "200" ]] && { SENT=$((SENT + 1)); break; }; sleep 2; done
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

step "waiting for the usage to reach the console (collector, OpenMeter's Kafka and sink, ClickHouse)"
want=$((before + SENT))
got="$before"
for attempt in $(seq 1 90); do
  got="$(curl -sf "$API/summary?range=30d" | python3 -c 'import sys, json; print(json.load(sys.stdin)["current"]["requests"])' || echo "$got")"
  echo "  $got of $want requests"
  [[ "$got" -ge "$want" ]] && break
  [[ $attempt -eq 90 ]] && { echo "ERROR: the console shows $got requests, expected $want. Sink: $KUBECTL -n $OM_NS logs deploy/${RELEASE}-openmeter-sink-worker" >&2; exit 1; }
  sleep 2
done

step "spend by person and by team, from the console's API (last 30 days)"
for by in person team; do
  curl -sf "$API/spend?range=30d&by=$by" | python3 -c '
import sys, json
for r in json.load(sys.stdin)["rows"]: print("  %-10s %3d requests %6d tokens  $%.4f" % (r["label"], r["requests"], r["tokens"], r["spendUsd"]))'
done
echo
echo "Console: $CONSOLE (this script's port-forward). To keep it open: $KUBECTL -n $OM_NS port-forward svc/${RELEASE}-openmeter-console 18892:80"
