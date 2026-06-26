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
fi

# --- 2. operator + key-manager --------------------------------------------
if ! kubectl -n "$NS" get deploy/llm-key-manager >/dev/null 2>&1; then
  echo "==> building images and deploying operator + key-manager..."
  make build-images
  make load-images
  make deploy
else
  echo "==> operator + key-manager already deployed."
fi

# --- 3. provider credential + models --------------------------------------
echo "==> applying OpenRouter credential and dev models..."
kubectl -n "$NS" create secret generic openrouter-api-key \
  --from-literal=apiKey="$OPENROUTER_API_KEY" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -f manifests/dev-models.yaml >/dev/null
for m in claude-sonnet-45 gemini-25-flash llama-33-70b; do
  kubectl -n "$NS" wait passthroughmodel/$m --for=jsonpath='{.status.phase}'=Ready --timeout=90s
done

# --- 4. port-forward + UI dev server --------------------------------------
cleanup() {
  echo
  echo "==> shutting down..."
  [[ -n "${PF_PID:-}" ]] && kill "$PF_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "==> port-forwarding key-manager to localhost:${KM_PORT}..."
kubectl -n "$NS" port-forward svc/llm-key-manager "${KM_PORT}:8080" >/tmp/km-portforward.log 2>&1 &
PF_PID=$!
for _ in $(seq 1 20); do
  if grep -q "Forwarding from" /tmp/km-portforward.log 2>/dev/null; then break; fi
  sleep 0.5
done

echo
echo "  key-manager (embedded UI + API): http://localhost:${KM_PORT}"
echo "  hot-reload UI dev server:        http://localhost:${UI_PORT}   <-- develop here"
echo "  edit key-manager/internal/ui/static/* and the browser reloads automatically."
echo "  Ctrl-C to stop."
echo

# Foreground: exits on Ctrl-C, which triggers cleanup of the port-forward.
( cd uidev && go run . \
    -static ../../key-manager/internal/ui/static \
    -api "http://localhost:${KM_PORT}" \
    -addr ":${UI_PORT}" )
