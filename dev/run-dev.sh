#!/usr/bin/env bash
# One-command local dev environment for the key-manager UI.
#
# Brings up (idempotently): a kind cluster with all dependencies, the operator,
# the dev-mode key-manager, and three OpenRouter passthrough models. Then it
# port-forwards the key-manager and starts a hot-reloading UI dev server.
#
# Only prerequisite: dev/.env with OPENROUTER_API_KEY set (see .env.example).
set -euo pipefail

cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
NS=llm-operator-system
KM_PORT="${KM_PORT:-8080}"
UI_PORT="${UI_PORT:-5173}"

# Pin kubectl to the kind cluster's context so this script can never act on
# whatever the current context happens to be (e.g. a remote/production cluster).
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"

# --- load .env -------------------------------------------------------------
if [[ -f .env ]]; then
  set -a; . ./.env; set +a
fi
if [[ -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "ERROR: OPENROUTER_API_KEY is not set. Copy dev/.env.example to dev/.env and fill it in." >&2
  exit 1
fi

# --- 1. cluster + dependencies --------------------------------------------
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  echo "==> kind cluster '$CLUSTER_NAME' not found; running full setup (a few minutes)..."
  make setup
else
  # The cluster is registered, but its node container(s) may be stopped (e.g.
  # after a Docker Desktop restart or OOM). `kind get clusters` still lists it,
  # so it slips past the check above and later kubectl / `kind load` calls fail
  # against a dead container. Start any stopped nodes and wait for the API.
  stopped="$(docker ps -aq \
    --filter "label=io.x-k8s.kind.cluster=${CLUSTER_NAME}" \
    --filter "status=exited" --filter "status=created" 2>/dev/null || true)"
  if [[ -n "$stopped" ]]; then
    echo "==> kind cluster '$CLUSTER_NAME' has stopped node(s); starting them..."
    # shellcheck disable=SC2086
    docker start $stopped >/dev/null
    echo "==> waiting for the Kubernetes API to become ready..."
    for _ in $(seq 1 30); do
      if $KUBECTL get --raw='/readyz' >/dev/null 2>&1; then break; fi
      sleep 2
    done
  fi
fi

# --- 2. operator + key-manager --------------------------------------------
if ! $KUBECTL -n "$NS" get deploy/llm-key-manager >/dev/null 2>&1; then
  echo "==> building images and deploying operator + key-manager..."
  make build-images
  make load-images
  make deploy
else
  echo "==> operator + key-manager already deployed."
fi

# --- 3. provider credential + models --------------------------------------
echo "==> applying OpenRouter credential and dev models..."
$KUBECTL -n "$NS" create secret generic openrouter-api-key \
  --from-literal=apiKey="$OPENROUTER_API_KEY" \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
# The operator's validating webhook gates PassthroughModel creates and isn't
# guaranteed to be serving the instant `make deploy`'s rollout returns (it waits
# on the cert mount). Retry so a momentary "connection refused" doesn't trip
# `set -e` and abort the whole run.
for attempt in $(seq 1 30); do
  $KUBECTL apply -f manifests/dev-models.yaml >/dev/null 2>&1 && break
  if [[ $attempt -eq 30 ]]; then
    echo "ERROR: operator webhook never became ready" >&2
    $KUBECTL apply -f manifests/dev-models.yaml >&2 || true
    exit 1
  fi
  echo "==> operator webhook not ready yet, retrying ($attempt)..."
  sleep 2
done
for m in claude-sonnet-45 gemini-25-flash llama-33-70b; do
  $KUBECTL -n "$NS" wait passthroughmodel/$m --for=jsonpath='{.status.phase}'=Ready --timeout=90s
done

# --- 4. port-forward + UI dev server --------------------------------------
cleanup() {
  echo
  echo "==> shutting down..."
  [[ -n "${PF_PID:-}" ]] && kill "$PF_PID" 2>/dev/null || true
  [[ -n "${PF_LOG:-}" ]] && rm -f "$PF_LOG"
}
trap cleanup EXIT INT TERM

# Fresh temp file per run: a fixed path could carry "Forwarding from" from a
# crashed prior run and make the readiness check below pass instantly.
PF_LOG="$(mktemp)"
echo "==> port-forwarding key-manager to localhost:${KM_PORT}..."
$KUBECTL -n "$NS" port-forward svc/llm-key-manager "${KM_PORT}:8080" >"$PF_LOG" 2>&1 &
PF_PID=$!
for _ in $(seq 1 20); do
  if grep -q "Forwarding from" "$PF_LOG" 2>/dev/null; then break; fi
  sleep 0.5
done

echo
echo "  key-manager API (dev mode, no auth): http://localhost:${KM_PORT}"
echo "  React UI dev server (hot reload):    http://localhost:${UI_PORT}   <-- develop here"
echo "  edit frontend/src/* and Vite hot-reloads the browser."
echo "  Ctrl-C to stop."
echo

# First run needs the frontend deps installed.
if [[ ! -d ../frontend/node_modules ]]; then
  echo "==> installing frontend dependencies (first run)..."
  npm --prefix ../frontend install
fi

# Foreground: the Vite dev server. Exits on Ctrl-C, which triggers cleanup of
# the port-forward. VITE_DEV_NO_AUTH bypasses the Keycloak login redirect to
# match the key-manager's LLM_DEV_MODE, so no Keycloak is required. Vite proxies
# /api to the port-forwarded key-manager (WEBAPI_URL; see frontend/vite.config.ts).
VITE_DEV_NO_AUTH=true WEBAPI_URL="http://localhost:${KM_PORT}" \
  npm --prefix ../frontend run dev -- --port "${UI_PORT}" --strictPort
