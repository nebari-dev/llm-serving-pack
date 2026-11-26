#!/usr/bin/env bash
# Put the dev cluster in the state the SPA's real-auth browser flow needs
# (`make spa-real-auth`):
#   - the dev Keycloak from the shared manifest (`make deploy-keycloak`), whose
#     token issuer follows the request host, so a login through
#     `make pf-keycloak` (http://localhost:8180) mints iss=http://localhost:8180/realms/nebari
#   - the key-manager out of dev mode, validating bearer tokens against that
#     issuer (manifests/spa-real-auth, applied with `kubectl apply`)
# Then: `make pf-keycloak`, `make pf-key-manager`, and `cd ../frontend && npm run dev`.
#
# Only Keycloak and the key-manager switch. Run after `make integration-up`, the
# rest of that profile stays (the operator on the shared Gateway with metering,
# the access log, ClientTrafficPolicy, usage collector and mock provider);
# `make integration-up` switches Keycloak and the key-manager back.
# `make deploy` returns the key-manager to dev mode.
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
export CLUSTER_NAME
NS=llm-operator-system
# Pinned to the kind cluster's context so this never acts on another cluster.
KUBECTL="kubectl --context kind-${CLUSTER_NAME}"

if ! $KUBECTL -n "$NS" get deploy/llm-key-manager >/dev/null 2>&1; then
  echo "ERROR: llm-key-manager is not deployed. Run 'make deploy' (or './run-dev.sh') first." >&2
  exit 1
fi

echo "==> deploying the dev Keycloak from the shared manifest (issuer follows the request host)"
make deploy-keycloak

echo "==> taking the key-manager out of dev mode, issuer http://localhost:8180"
kubectl kustomize --load-restrictor=LoadRestrictionsNone manifests/spa-real-auth | $KUBECTL apply -f -
$KUBECTL -n "$NS" rollout status deploy/llm-key-manager --timeout=180s

cat <<'EOF'

==> real-auth mode is on. For the SPA login:
      make pf-keycloak       # http://localhost:8180 (log in as dev / password)
      make pf-key-manager    # http://localhost:8080
      cd ../frontend && npm run dev
    `make deploy` switches the key-manager back to dev mode.
EOF
