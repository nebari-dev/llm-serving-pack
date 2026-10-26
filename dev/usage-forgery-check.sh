#!/usr/bin/env bash
# Checks, on the local kind cluster, that a caller cannot choose the identity
# or the request id the gateway's usage access log records.
#
# Usage is billed from the access log's client_id (API keys) and auth_*
# (Keycloak tokens) fields, which are request headers. Each case below sends
# hand-written x-llm-client-id, x-auth-user, x-auth-subject, x-auth-groups and
# x-request-id headers and then reads the gateway's own copy of the access log
# (its stdout sink) to see what was recorded:
#
#   1. invalid API key            -> 401; no identity recorded
#   2. invalid token              -> 401; no identity recorded
#   3. valid token, no groups     -> recorded user and subject are the token's;
#                                    no groups recorded (the forged ones are gone)
#   4. valid token (alice)        -> recorded user, subject, groups are alice's
#   5. valid API key (alice)      -> recorded client ID is the key's
#   6. invalid API key, sent from inside a pod (an in-cluster caller)
#   7. valid API key, bodies over Envoy's 32KiB default buffer -> 200, when a
#      Gateway-wide ClientTrafficPolicy raises nebari-gateway's connection
#      buffer limit (as NIC's does): the listener policy that removes the
#      headers must carry that limit, or the body is rejected with 413. A
#      48KiB body over HTTP/2 (each HTTP/2 stream is also held to Envoy
#      Gateway's 64KiB default stream window) and a 1MiB body over HTTP/1.1
#
# In every case the recorded request id must be one the gateway made, not the
# one sent, and no forged group may be recorded.
#
# Prereq: `make usage-e2e` has run (mock provider, metering on, access log).
# The identity headers are removed by the ClientTrafficPolicy the operator
# creates on the pack's listeners when usage metering is on. Leaves nothing
# behind: the temporary Keycloak user and API key are deleted on exit.
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
NS=llm-operator-system
GW_NS=envoy-gateway-system
MODEL_RESOURCE=mock-provider
MODEL=mock/small
EXT_PORT="${EXT_PORT:-28443}"
INT_PORT="${INT_PORT:-29443}"
KM_PORT="${KM_PORT:-28080}"
# Pinned to the kind cluster's context so this never acts on another cluster.
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"

RUN="forgery-$(openssl rand -hex 4)"
FORGED_CLIENT="user-alice-forged-$RUN"
FORGED_SUBJECT="sub-forged-$RUN"
FORGED_USER="user-forged-$RUN"
FORGED_GROUPS="team-forged-$RUN"
TEMP_USER="nogroups-$RUN"

if ! $KUBECTL -n "$NS" get passthroughmodel "$MODEL_RESOURCE" >/dev/null 2>&1; then
  echo "ERROR: passthroughmodel/$MODEL_RESOURCE not found; run 'make usage-e2e' first." >&2
  exit 1
fi

# --- cleanup ---------------------------------------------------------------------------
PIDS=()
KEY_CLIENT_ID=""
ALICE_TOKEN=""
cleanup() {
  set +e
  if [[ -n "$KEY_CLIENT_ID" && -n "$ALICE_TOKEN" ]]; then
    # Retried: the key-manager may be restarting under another deploy.
    local revoked=""
    for _ in $(seq 1 10); do
      code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -H "Authorization: Bearer $ALICE_TOKEN" \
        "http://localhost:$KM_PORT/api/keys/$NS/$MODEL_RESOURCE/$KEY_CLIENT_ID")"
      [[ "$code" == 2* || "$code" == 404 ]] && { revoked=1; break; }
      forward_all; sleep 2
    done
    [[ -n "$revoked" ]] || echo "WARNING: could not revoke alice's check key $KEY_CLIENT_ID; revoke it in the key manager" >&2
  fi
  kc_admin "delete-user" "$TEMP_USER" "" >/dev/null 2>&1
  [[ -n "${SEEN_FILE:-}" ]] && rm -f "$SEEN_FILE"
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null; done
}
trap cleanup EXIT INT TERM

# --- Keycloak: a temporary user in no group -------------------------------------------
# Same pattern as demo-users.sh: passwords go over stdin, never on a command line.
kc_admin() { # action username password
  local admin_pw
  admin_pw="$($KUBECTL -n keycloak get deploy keycloak -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="KC_BOOTSTRAP_ADMIN_PASSWORD")].value}')"
  printf '%s\n%s\n%s\n%s\n' "$admin_pw" "$1" "$2" "$3" | $KUBECTL -n keycloak exec -i deploy/keycloak -- bash -c '
set -e
read -r ADMIN_PW; read -r ACTION; read -r NAME; read -r PW
KC=/opt/keycloak/bin/kcadm.sh
$KC config credentials --server http://localhost:8080 --realm master --user admin --password "$ADMIN_PW" >/dev/null
id=$($KC get users -r nebari -q username=$NAME -q exact=true --fields id | sed -n "s/.*\"id\" *: *\"\([^\"]*\)\".*/\1/p")
case $ACTION in
  create-user)
    [ -n "$id" ] || $KC create users -r nebari -s username=$NAME -s enabled=true -s email=$NAME@example.com \
      -s emailVerified=true -s firstName=$NAME -s lastName=check >/dev/null
    $KC set-password -r nebari --username $NAME --new-password "$PW" >/dev/null ;;
  delete-user)
    [ -z "$id" ] || $KC delete users/$id -r nebari ;;
esac'
}
userpw() { $KUBECTL -n keycloak get secret demo-users -o jsonpath="{.data.$1}" | base64 -d; }
token_for() { # user password -> access token (minted inside the cluster: in-cluster issuer)
  $KUBECTL -n "$NS" exec -i deploy/mock-provider -- python -c '
import json, sys, urllib.request, urllib.parse
user, pw = sys.stdin.read().split("\n")[:2]
data = urllib.parse.urlencode({"client_id": "nebari-frontend-spa", "username": user, "password": pw, "grant_type": "password"}).encode()
print(json.load(urllib.request.urlopen("http://keycloak.keycloak.svc.cluster.local:8080/realms/nebari/protocol/openid-connect/token", data))["access_token"])' <<<"$1
$2"
}
token_sub() { python3 -c 'import sys, json, base64; p = sys.stdin.read().split(".")[1]; print(json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4)))["sub"])' <<<"$1"; }

# --- port-forwards ----------------------------------------------------------------------
gateway_svc() { $KUBECTL -n "$GW_NS" get svc -l gateway.envoyproxy.io/owning-gateway-name="$1" -o jsonpath='{.items[0].metadata.name}'; }
EXT_SVC="$(gateway_svc nebari-gateway)"
INT_SVC="$(gateway_svc nebari-internal-gateway)"
forward_all() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  PIDS=()
  $KUBECTL -n "$GW_NS" port-forward "svc/$EXT_SVC" "$EXT_PORT:443" >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$GW_NS" port-forward "svc/$INT_SVC" "$INT_PORT:443" >/dev/null 2>&1 & PIDS+=($!)
  $KUBECTL -n "$NS" port-forward svc/llm-key-manager "$KM_PORT:8080" >/dev/null 2>&1 & PIDS+=($!)
  sleep 2
}
forward_all

BODY="{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
# send host port bearer request-id -> HTTP status. Sends every forged header,
# and x-ai-eg-model so that a request rejected before the AI Gateway reads its
# body is still in the access log. Retried on HTTP 000 (a dropped port-forward).
send() {
  local host="$1" port="$2" bearer="$3" rid="$4" body="${5:-$BODY}" proto="${6:---http2}" code=000
  for _ in 1 2 3 4 5; do
    code="$(curl "$proto" -sk -o /dev/null -w '%{http_code}' --max-time 20 --resolve "$host:$port:127.0.0.1" \
      -H "Authorization: Bearer $bearer" -H 'Content-Type: application/json' -H "x-ai-eg-model: $MODEL" \
      -H "x-llm-client-id: $FORGED_CLIENT" -H "x-auth-subject: $FORGED_SUBJECT" \
      -H "x-auth-user: $FORGED_USER" -H "x-auth-groups: $FORGED_GROUPS" -H "x-request-id: $rid" \
      --data-binary @- "https://$host:$port/v1/chat/completions" <<<"$body" || true)"
    [[ "$code" != "000" ]] && break
    forward_all
  done
  echo "$code"
}

# entry gateway since route-suffix status -> the one access-log record (JSON)
# for the request just sent, read from the gateway's stdout copy. Records
# already matched by an earlier case are skipped.
# Request ids already matched, in a file: entry runs in a command substitution
# (a subshell), so a variable it set would be lost.
SEEN_FILE="$(mktemp)"
entry() {
  local gw="$1" since="$2" suffix="$3" status="$4" out
  for _ in $(seq 1 15); do
    out="$($KUBECTL -n "$GW_NS" logs -l gateway.envoyproxy.io/owning-gateway-name="$gw" -c envoy --tail=-1 --since-time="$since" 2>/dev/null \
      | python3 -c '
import json, sys
suffix, status, seen = sys.argv[1], sys.argv[2], set(sys.argv[3].split())
found = []
for line in sys.stdin:
    if not line.startswith("{"):
        continue
    try:
        e = json.loads(line)
    except ValueError:
        continue
    route = (e.get("route") or "").split("/")
    if len(route) > 2 and route[2].endswith(suffix) and str(e.get("response_code")) == status and e.get("request_id") not in seen:
        found.append(e)
if len(found) == 1:
    print(json.dumps(found[0]))
elif len(found) > 1:
    print("AMBIGUOUS")
' "$suffix" "$status" "$(cat "$SEEN_FILE")")"
    if [[ "$out" == "AMBIGUOUS" ]]; then
      echo "ERROR: other traffic on $gw matched too; re-run when the gateway is idle" >&2; exit 1
    fi
    if [[ -n "$out" ]]; then
      python3 -c 'import sys, json; print(json.loads(sys.argv[1])["request_id"])' "$out" >>"$SEEN_FILE"
      echo "$out"; return 0
    fi
    sleep 1
  done
  echo "ERROR: no access-log record on $gw for the request (route *$suffix, HTTP $status)" >&2
  exit 1
}

FAILED=0
# check name record sent-request-id expectations... ; each expectation is
# field=value, field=ABSENT, or groups-not-forged / groups-include:<g>.
check() {
  local name="$1" record="$2" rid="$3"; shift 3
  if ! python3 - "$name" "$record" "$rid" "$FORGED_GROUPS" "$@" <<'PY'; then FAILED=$((FAILED + 1)); fi
import base64, json, sys
name, record, rid, forged_groups = sys.argv[1:5]
e = json.loads(record)
problems = []
def groups(v):
    try:
        return json.loads(base64.b64decode(v))
    except Exception:
        return [g.strip() for g in v.split(",")]
if e.get("request_id") == rid:
    problems.append("request_id is the one the caller sent")
for exp in sys.argv[5:]:
    field, _, want = exp.partition("=")
    got = e.get(field)
    if want == "ABSENT":
        if got not in (None, "-"):
            problems.append(f"{field} = {got!r}, want absent")
    elif field == "auth_groups" and want.startswith("include:"):
        if got in (None, "-") or want[len("include:"):] not in groups(got):
            problems.append(f"auth_groups = {got!r}, want the token's groups")
    elif field == "auth_groups" and want == "not-forged":
        if got not in (None, "-") and forged_groups in groups(got):
            problems.append(f"auth_groups = {got!r} holds the forged group")
    elif str(got) != want:
        problems.append(f"{field} = {got!r}, want {want!r}")
fields = {k: e.get(k) for k in ("response_code", "response_code_details", "client_id", "auth_user", "auth_subject", "auth_groups", "request_id")}
print(("FAIL" if problems else "ok  "), name, json.dumps(fields))
for p in problems:
    print("      -", p)
sys.exit(1 if problems else 0)
PY
}
since() { date -u -v-2S +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '-2 seconds' +%Y-%m-%dT%H:%M:%SZ; }

# --- callers -------------------------------------------------------------------------------
echo "==> logging in alice and a temporary user in no group ($TEMP_USER)"
ALICE_TOKEN="$(token_for alice "$(userpw alice)")"
ALICE_SUB="$(token_sub "$ALICE_TOKEN")"
TEMP_PW="$(openssl rand -base64 24)"
kc_admin create-user "$TEMP_USER" "$TEMP_PW" >/dev/null
TEMP_TOKEN="$(token_for "$TEMP_USER" "$TEMP_PW")"
TEMP_SUB="$(token_sub "$TEMP_TOKEN")"
unset TEMP_PW

echo "==> minting an API key for alice and waiting for the gateway to accept it"
created="$(curl -sf -X POST "http://localhost:$KM_PORT/api/keys" -H "Authorization: Bearer $ALICE_TOKEN" \
  -H 'Content-Type: application/json' -d "{\"modelName\":\"$MODEL_RESOURCE\",\"description\":\"forgery check $RUN\"}")"
KEY_CLIENT_ID="$(python3 -c 'import sys, json; print(json.load(sys.stdin)["clientId"])' <<<"$created")"
ALICE_KEY="$(python3 -c 'import sys, json; print(json.load(sys.stdin)["apiKey"])' <<<"$created")"
unset created
for attempt in $(seq 1 120); do
  t="$(since)"
  code="$(send llm.local "$EXT_PORT" "$ALICE_KEY" "$RUN-warmup-$attempt")"
  [[ "$code" == "200" ]] && { entry nebari-gateway "$t" -external 200 >/dev/null; break; }
  [[ $attempt -eq 120 ]] && { echo "ERROR: the gateway never accepted the new key (last: HTTP $code)" >&2; exit 1; }
  sleep 2
done

echo "==> sending forged identity headers"
rid="$RUN-1"; t="$(since)"; code="$(send llm.local "$EXT_PORT" sk-not-a-key "$rid")"
check "1 invalid API key (HTTP $code)" "$(entry nebari-gateway "$t" -external "$code")" "$rid" \
  response_code=401 client_id=ABSENT auth_user=ABSENT auth_subject=ABSENT auth_groups=ABSENT

rid="$RUN-2"; t="$(since)"; code="$(send llm-internal.local "$INT_PORT" "eyJhbGciOiJSUzI1NiJ9.e30.invalid" "$rid")"
check "2 invalid token (HTTP $code)" "$(entry nebari-internal-gateway "$t" -internal "$code")" "$rid" \
  response_code=401 client_id=ABSENT auth_user=ABSENT auth_subject=ABSENT auth_groups=ABSENT

rid="$RUN-3"; t="$(since)"; code="$(send llm-internal.local "$INT_PORT" "$TEMP_TOKEN" "$rid")"
check "3 valid token, no groups (HTTP $code)" "$(entry nebari-internal-gateway "$t" -internal "$code")" "$rid" \
  client_id=ABSENT auth_user="$TEMP_USER" auth_subject="$TEMP_SUB" auth_groups=not-forged

rid="$RUN-4"; t="$(since)"; code="$(send llm-internal.local "$INT_PORT" "$ALICE_TOKEN" "$rid")"
check "4 valid token, alice (HTTP $code)" "$(entry nebari-internal-gateway "$t" -internal "$code")" "$rid" \
  response_code=200 client_id=ABSENT auth_user=alice auth_subject="$ALICE_SUB" auth_groups=include:team-a auth_groups=not-forged

rid="$RUN-5"; t="$(since)"; code="$(send llm.local "$EXT_PORT" "$ALICE_KEY" "$rid")"
check "5 valid API key, alice (HTTP $code)" "$(entry nebari-gateway "$t" -external "$code")" "$rid" \
  response_code=200 client_id="$KEY_CLIENT_ID" auth_user=ABSENT auth_subject=ABSENT auth_groups=ABSENT

# An in-cluster caller: Envoy may treat a pod address as internal and keep its
# x-request-id, so this one is sent from the mock provider's pod.
rid="$RUN-6"; t="$(since)"
code="$($KUBECTL -n "$NS" exec -i deploy/mock-provider -- python - "$EXT_SVC.$GW_NS.svc" llm.local "$rid" \
  "$FORGED_CLIENT" "$FORGED_SUBJECT" "$FORGED_USER" "$FORGED_GROUPS" "$MODEL" <<'PY'
import http.client, socket, ssl, sys
addr, host, rid, client, subject, user, groups, model = sys.argv[1:9]
ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE  # the dev gateway's certificate is self-signed
conn = http.client.HTTPSConnection(host, 443)
conn.sock = ctx.wrap_socket(socket.create_connection((addr, 443), timeout=20), server_hostname=host)
conn.request("POST", "/v1/chat/completions", body='{"model":"%s","messages":[]}' % model, headers={
    "Host": host, "Authorization": "Bearer sk-not-a-key", "Content-Type": "application/json",
    "x-ai-eg-model": model, "x-request-id": rid, "x-llm-client-id": client,
    "x-auth-subject": subject, "x-auth-user": user, "x-auth-groups": groups})
print(conn.getresponse().status)
PY
)"
check "6 invalid API key from a pod (HTTP $code)" "$(entry nebari-gateway "$t" -external "$code")" "$rid" \
  response_code=401 client_id=ABSENT auth_user=ABSENT auth_subject=ABSENT auth_groups=ABSENT

# Gateway-wide policies (no sectionName) on nebari-gateway that set a buffer limit.
GW_BUFFER="$($KUBECTL -n "$GW_NS" get clienttrafficpolicy -o json | python3 -c '
import json, sys
for p in json.load(sys.stdin)["items"]:
    refs = p["spec"].get("targetRefs", [])
    if any(r.get("name") == "nebari-gateway" and not r.get("sectionName") for r in refs) and p["spec"].get("connection", {}).get("bufferLimit"):
        print(p["metadata"]["name"], p["spec"]["connection"]["bufferLimit"])')"
if [[ -z "$GW_BUFFER" ]]; then
  echo "skip 7 large body: no Gateway-wide ClientTrafficPolicy sets a connection buffer limit on nebari-gateway"
else
  large_body() { python3 -c 'import json, sys; print(json.dumps({"model": sys.argv[1], "messages": [{"role": "user", "content": "x" * int(sys.argv[2])}]}))' "$MODEL" "$1"; }
  for c in "7a 49152 --http2" "7b 1048576 --http1.1"; do
    read -r n size proto <<<"$c"
    body="$(large_body "$size")"
    rid="$RUN-$n"; t="$(since)"; code="$(send llm.local "$EXT_PORT" "$ALICE_KEY" "$rid" "$body" "$proto")"
    check "$n valid API key, ${#body}-byte body over ${proto#--}, Gateway buffer limit $GW_BUFFER (HTTP $code)" "$(entry nebari-gateway "$t" -external "$code")" "$rid" \
      response_code=200 client_id="$KEY_CLIENT_ID" auth_user=ABSENT auth_subject=ABSENT auth_groups=ABSENT
  done
fi

echo
if [[ $FAILED -gt 0 ]]; then
  echo "FAILED: $FAILED case(s) above" >&2
  exit 1
fi
echo "PASS: no caller-chosen identity or request id reached the access log"
