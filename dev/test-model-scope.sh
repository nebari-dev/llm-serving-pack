#!/usr/bin/env bash
# Prove per-model API-key scoping AND that a client cannot spoof the
# x-llm-client-id authorization header (issues #116 / #122; PR #117 security
# assessment finding R-04) end-to-end through the real gateway data path.
#
# Envoy Gateway authenticates once on the shared external listener (every
# model's api-keys Secret is pooled into credentialRefs), then each model's
# HTTPRoute carries a deny-by-default SecurityPolicy whose single Allow rule
# lists only that model's client IDs, matched on the x-llm-client-id header.
# The api_key_auth filter populates that header with setReferenceKey (a
# replace-all "set"), so any client-supplied x-llm-client-id - duplicate,
# mixed-case, or comma-joined - is discarded before the authorization filter
# runs. This script locks that behavior in against a gateway/Envoy version bump.
#
# It deploys two served mock models (scope-a, scope-b), injects a known API key
# into each model's api-keys Secret (skipping the key-manager, exactly as
# local-development.md does), then drives the external gateway with a request
# matrix. The security-critical assertions are the negative ones (403/401): the
# gateway returns those from its authorization filter before proxying to any
# backend, so they hold even if a mock backend is not ready.
#
#   key for A            -> model A                    : 200  (positive control)
#   key for A            -> model B                    : 403  (cross-model denied)
#   key for A + spoof x-llm-client-id=<B id> -> B      : 403  (R-04: header ignored)
#   key for A + duplicate spoof header       -> B      : 403  (R-04)
#   key for A + mixed-case spoof header      -> B      : 403  (R-04)
#   key for A + comma-joined spoof value     -> B      : 403  (R-04)
#   key for B            -> model B                    : 200  (positive control)
#   invalid key          -> model B                    : 401
#
# Cleanup deletes the two test models on exit. Set KEEP=1 to leave them.
set -euo pipefail

cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
NS=llm-operator-system
GW_NS=envoy-gateway-system
GW_NAME=nebari-gateway
GW_PORT="${GW_PORT:-8443}"
HOST="llm.${LLM_BASE_DOMAIN:-local}"

# Pin kubectl to the kind cluster's context so this can never act on whatever
# the current context happens to be (e.g. a remote/production cluster).
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"

# Two served mock models. The x-ai-eg-model header the AI Gateway derives from
# the request body's "model" field is spec.model.name, so that is what we send.
MODEL_A=scope-a
MODEL_B=scope-b
SERVE_A="test/scope-a"
SERVE_B="test/scope-b"

# Client IDs and keys we inject directly into each model's api-keys Secret. We
# choose the client IDs so we know B's id to attempt to spoof it from A.
CID_A=clienta
CID_B=clientb
KEY_A="sk-scope-a-$(printf 'a%.0s' {1..24})"
KEY_B="sk-scope-b-$(printf 'b%.0s' {1..24})"

# --- preflight -------------------------------------------------------------
if ! $KUBECTL -n "$NS" get deploy/llm-operator >/dev/null 2>&1; then
  echo "ERROR: operator is not deployed. Run 'make deploy' (or './run-dev.sh') first." >&2
  exit 1
fi
if ! $KUBECTL -n "$GW_NS" get gateway "$GW_NAME" >/dev/null 2>&1; then
  echo "ERROR: gateway '$GW_NAME' not found. Run 'make setup' first." >&2
  exit 1
fi

# --- deploy two served mock models -----------------------------------------
apply_model() { # $1 = metadata.name  $2 = spec.model.name
  $KUBECTL apply -f - >/dev/null <<YAML
apiVersion: llm.nebari.dev/v1alpha1
kind: LLMModel
metadata:
  name: $1
  namespace: $NS
spec:
  model:
    name: "$2"
    source: huggingface
    storage:
      type: emptyDir
      size: 1Gi
    preload: false
  resources:
    gpu:
      count: 0
      type: nvidia
    requests: { cpu: "100m", memory: "128Mi" }
    limits: { cpu: "200m", memory: "256Mi" }
  serving:
    image: mock-vllm:dev
    command: ["python", "/server.py"]
    replicas: 1
    monitoring: { enabled: false }
  access:
    public: true
  endpoints:
    external: { enabled: true, subdomain: $1 }
    internal: { enabled: true }
YAML
}

echo "==> deploying mock models '$MODEL_A' and '$MODEL_B'..."
# The validating webhook may not be serving the instant the operator rollout
# returns (it waits on the cert mount); retry so a momentary refusal does not
# trip set -e.
for attempt in $(seq 1 30); do
  if apply_model "$MODEL_A" "$SERVE_A" && apply_model "$MODEL_B" "$SERVE_B"; then break; fi
  [[ $attempt -eq 30 ]] && { echo "ERROR: operator webhook never became ready" >&2; exit 1; }
  sleep 2
done

# --- cleanup ---------------------------------------------------------------
PF_PID=""; PF_LOG=""
cleanup() {
  [[ -n "$PF_PID" ]] && kill "$PF_PID" 2>/dev/null || true
  [[ -n "$PF_LOG" ]] && rm -f "$PF_LOG" 2>/dev/null || true
  if [[ "${KEEP:-0}" != "1" ]]; then
    echo "==> deleting mock models..."
    $KUBECTL -n "$NS" delete llmmodel "$MODEL_A" "$MODEL_B" --ignore-not-found >/dev/null 2>&1 || true
  else
    echo "==> KEEP=1: leaving mock models in place."
  fi
}
trap cleanup EXIT INT TERM

echo "==> waiting for models to report Ready..."
$KUBECTL -n "$NS" wait llmmodel/"$MODEL_A" --for=jsonpath='{.status.phase}'=Ready --timeout=180s
$KUBECTL -n "$NS" wait llmmodel/"$MODEL_B" --for=jsonpath='{.status.phase}'=Ready --timeout=180s

# --- inject known API keys into each model's api-keys Secret ----------------
# The operator creates <model>-api-keys as an empty Opaque Secret; we add a
# data key (the client ID) whose value is the API key, exactly as the
# key-manager would. Patching the Secret re-triggers the operator to re-render
# both models' SecurityPolicies (pooled credentialRefs + per-model allow-list).
echo "==> injecting API keys (client IDs '$CID_A', '$CID_B')..."
$KUBECTL -n "$NS" patch secret "${MODEL_A}-api-keys" --type merge \
  -p "{\"stringData\":{\"${CID_A}\":\"${KEY_A}\"}}" >/dev/null
$KUBECTL -n "$NS" patch secret "${MODEL_B}-api-keys" --type merge \
  -p "{\"stringData\":{\"${CID_B}\":\"${KEY_B}\"}}" >/dev/null

# --- port-forward the external gateway --------------------------------------
SVC="$($KUBECTL -n "$GW_NS" get svc \
  -l gateway.envoyproxy.io/owning-gateway-name="$GW_NAME" -o name | head -1)"
if [[ -z "$SVC" ]]; then
  echo "ERROR: could not find the Envoy service for gateway '$GW_NAME'." >&2
  exit 1
fi
PF_LOG="$(mktemp)"
echo "==> port-forwarding $SVC :$GW_PORT -> 443..."
$KUBECTL -n "$GW_NS" port-forward "$SVC" "${GW_PORT}:443" >"$PF_LOG" 2>&1 &
PF_PID=$!
for _ in $(seq 1 40); do
  grep -q "Forwarding from" "$PF_LOG" 2>/dev/null && break
  sleep 0.5
done

# --- request helpers --------------------------------------------------------
GW="https://${HOST}:${GW_PORT}/v1/chat/completions"
RESOLVE="${HOST}:${GW_PORT}:127.0.0.1"

# req MODEL BEARER [extra curl args...] -> echoes HTTP status code
req() {
  local model="$1" bearer="$2"; shift 2
  curl -sk -o /dev/null -w '%{http_code}' --max-time 10 \
    --resolve "$RESOLVE" \
    -H "Authorization: Bearer ${bearer}" \
    -H "Content-Type: application/json" \
    "$@" \
    -d "{\"model\":\"${model}\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}" \
    "$GW"
}

FAILED=0
check() { # name expected actual
  if [[ "$2" == "$3" ]]; then
    echo "  PASS: $1 (HTTP $3)"
  else
    echo "  FAIL: $1 - expected $2, got $3"
    FAILED=1
  fi
}

# --- wait for the gateway + allow-list to be ready --------------------------
# The positive control returns 200 only once THREE things converge: the HTTPS
# listener is Programmed (on a cold cluster this alone can take ~100s), Envoy
# Gateway has synced the api-keys Secret for authentication, and the operator
# has re-rendered the model's authorization allow-list. Poll it until 200 (or
# give up after ~4m and let the assertions report the failure).
echo "==> waiting for the gateway and SecurityPolicy allow-list to converge..."
for _ in $(seq 1 120); do
  [[ "$(req "$SERVE_A" "$KEY_A")" == "200" ]] && break
  sleep 2
done

# --- assertions -------------------------------------------------------------
echo "==> running per-model scope + header-spoofing matrix against $GW"
check "A's key -> model A (control)"                200 "$(req "$SERVE_A" "$KEY_A")"
check "B's key -> model B (control)"                200 "$(req "$SERVE_B" "$KEY_B")"
check "A's key -> model B (cross-model denied)"     403 "$(req "$SERVE_B" "$KEY_A")"
check "invalid key -> model B"                      401 "$(req "$SERVE_B" "not-a-real-key")"

# R-04: a client-supplied x-llm-client-id must never influence authorization.
check "spoof: A's key + x-llm-client-id=B -> model B" \
  403 "$(req "$SERVE_B" "$KEY_A" -H "x-llm-client-id: ${CID_B}")"
check "spoof: A's key + duplicate x-llm-client-id=B -> model B" \
  403 "$(req "$SERVE_B" "$KEY_A" -H "x-llm-client-id: ${CID_B}" -H "x-llm-client-id: ${CID_B}")"
check "spoof: A's key + mixed-case X-LLM-CLIENT-ID=B -> model B" \
  403 "$(req "$SERVE_B" "$KEY_A" -H "X-LLM-CLIENT-ID: ${CID_B}")"
check "spoof: A's key + comma-joined x-llm-client-id=A,B -> model B" \
  403 "$(req "$SERVE_B" "$KEY_A" -H "x-llm-client-id: ${CID_A},${CID_B}")"

echo
if [[ "$FAILED" -eq 0 ]]; then
  echo "==> PASS: per-model API-key scoping holds and x-llm-client-id cannot be spoofed."
else
  echo "==> FAIL: see assertions above." >&2
fi
exit "$FAILED"
