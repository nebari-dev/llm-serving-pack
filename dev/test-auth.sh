#!/usr/bin/env bash
# Exercise the key-manager's real bearer-token → Keycloak JWKS validation path
# on the local kind cluster (Model B). The everyday `run-dev.sh` loop bypasses
# auth (LLM_DEV_MODE + VITE_DEV_NO_AUTH), so this is the only local check that
# actually runs internal/api/jwt_validator.go end-to-end.
#
# It: deploys the dev Keycloak, flips the running key-manager out of dev mode
# into real bearer validation (JWKS fetched in-cluster, issuer = the
# port-forwarded Keycloak URL the token is minted from), mints a token via the
# public SPA client's direct-access grant, and asserts:
#   - valid token            → 200
#   - no Authorization header → 401
#   - garbage token          → 401
#
# By default it reverts the key-manager to dev mode on exit so the normal
# run-dev loop keeps working. Set KEEP_AUTH=1 to leave it in real-auth mode
# (e.g. to then test the SPA login flow with `cd ../frontend && npm run dev`).
set -euo pipefail

cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
NS=llm-operator-system
KM_PORT="${KM_PORT:-8080}"
KC_PORT="${KC_PORT:-8180}"
REALM=nebari
CLIENT_ID=nebari-frontend-spa
KC_USER="${KC_USER:-dev}"
KC_PASS="${KC_PASS:-password}"

# Pin kubectl to the kind cluster's context so this can never act on whatever
# the current context happens to be (e.g. a remote/production cluster).
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"

# --- preflight -------------------------------------------------------------
if ! $KUBECTL -n "$NS" get deploy/llm-key-manager >/dev/null 2>&1; then
  echo "ERROR: llm-key-manager is not deployed. Run 'make deploy' (or './run-dev.sh') first." >&2
  exit 1
fi

echo "==> ensuring dev Keycloak is deployed..."
if ! $KUBECTL -n keycloak get deploy/keycloak >/dev/null 2>&1; then
  make deploy-keycloak
else
  $KUBECTL -n keycloak rollout status deploy/keycloak --timeout=180s
fi

# --- flip key-manager into real-auth mode ----------------------------------
# JWKS is fetched over in-cluster DNS; the issuer must match the `iss` in tokens
# minted via the port-forward below (http://localhost:$KC_PORT).
echo "==> switching key-manager to real bearer-token validation..."
$KUBECTL -n "$NS" set env deploy/llm-key-manager \
  LLM_DEV_MODE- \
  LLM_KEYCLOAK_URL=http://keycloak.keycloak.svc.cluster.local:8080 \
  LLM_KEYCLOAK_REALM="$REALM" \
  LLM_KEYCLOAK_ISSUER_URL="http://localhost:${KC_PORT}"
$KUBECTL -n "$NS" rollout status deploy/llm-key-manager --timeout=90s

# --- cleanup: kill port-forwards, revert to dev mode unless KEEP_AUTH -------
KM_PF_PID=""; KC_PF_PID=""; PF_LOG=""
cleanup() {
  [[ -n "$KM_PF_PID" ]] && kill "$KM_PF_PID" 2>/dev/null || true
  [[ -n "$KC_PF_PID" ]] && kill "$KC_PF_PID" 2>/dev/null || true
  [[ -n "$PF_LOG" ]] && rm -f "$PF_LOG" 2>/dev/null || true
  if [[ "${KEEP_AUTH:-0}" != "1" ]]; then
    echo "==> reverting key-manager to dev mode..."
    $KUBECTL -n "$NS" set env deploy/llm-key-manager LLM_DEV_MODE=true >/dev/null
  else
    echo "==> KEEP_AUTH=1: leaving key-manager in real-auth mode."
  fi
}
trap cleanup EXIT INT TERM

# --- port-forwards ---------------------------------------------------------
PF_LOG="$(mktemp)"
echo "==> port-forwarding key-manager (:${KM_PORT}) and Keycloak (:${KC_PORT})..."
$KUBECTL -n "$NS" port-forward svc/llm-key-manager "${KM_PORT}:8080" >"$PF_LOG" 2>&1 &
KM_PF_PID=$!
$KUBECTL -n keycloak port-forward svc/keycloak "${KC_PORT}:8080" >>"$PF_LOG" 2>&1 &
KC_PF_PID=$!

# Wait for both forwards to report "Forwarding from" (two lines).
for _ in $(seq 1 40); do
  [[ "$(grep -c "Forwarding from" "$PF_LOG" 2>/dev/null || echo 0)" -ge 2 ]] && break
  sleep 0.5
done

# --- mint a token via the public SPA client's direct-access grant ----------
echo "==> minting an access token for '${KC_USER}'..."
TOKEN=""
for _ in $(seq 1 20); do
  RESP="$(curl -s \
    -d "client_id=${CLIENT_ID}" -d "username=${KC_USER}" -d "password=${KC_PASS}" \
    -d grant_type=password \
    "http://localhost:${KC_PORT}/realms/${REALM}/protocol/openid-connect/token" || true)"
  TOKEN="$(printf '%s' "$RESP" | grep -o '"access_token":"[^"]*"' | sed 's/"access_token":"//;s/"$//' || true)"
  [[ -n "$TOKEN" ]] && break
  sleep 1
done
if [[ -z "$TOKEN" ]]; then
  echo "ERROR: could not obtain an access token from Keycloak. Last response:" >&2
  printf '%s\n' "$RESP" >&2
  exit 1
fi

# --- assertions ------------------------------------------------------------
FAILED=0
code() { # $1 = curl args...; echoes the HTTP status
  curl -s -o /dev/null -w '%{http_code}' "$@"
}
check() { # name expected actual
  if [[ "$2" == "$3" ]]; then
    echo "  PASS: $1 (HTTP $3)"
  else
    echo "  FAIL: $1 — expected $2, got $3"
    FAILED=1
  fi
}

echo "==> running assertions against http://localhost:${KM_PORT}/api/models"
check "valid token accepted"   200 "$(code -H "Authorization: Bearer ${TOKEN}" "http://localhost:${KM_PORT}/api/models")"
check "missing token rejected" 401 "$(code "http://localhost:${KM_PORT}/api/models")"
check "garbage token rejected" 401 "$(code -H "Authorization: Bearer not.a.jwt" "http://localhost:${KM_PORT}/api/models")"

echo
if [[ "$FAILED" -eq 0 ]]; then
  echo "==> PASS: bearer-token JWKS validation is working."
else
  echo "==> FAIL: see assertions above." >&2
fi
exit "$FAILED"
