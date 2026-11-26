#!/usr/bin/env bash
# Bring the dev kind cluster to the `kind-dev` profile the journey suite's
# integration tier runs on (`make integration-up`), and write its provisioning
# record (ConfigMap llm-journeys/provision).
#
# The profile is one fixed state, applied with `kubectl apply` of the overlays
# under manifests/ (the gateway's EnvoyProxy is replaced whole), never
# `kubectl set env` or `patch`, so `make deploy` puts
# the cluster back in dev mode and `make deploy-keycloak` removes the Keycloak
# issuer pin:
#   - operator and key-manager built from the working tree, the key-manager
#     out of dev mode, both trusting the dev Keycloak at its in-cluster URL;
#     usage metering on, one shared Gateway (nebari-gateway)
#   - dev Keycloak with KC_HOSTNAME pinned to its in-cluster URL, so a token
#     minted through a port-forward carries the same `iss`; alice and bob
#   - the mock provider and its PassthroughModel, trusted through the CA
#     ConfigMap mock-provider-ca (spec.provider.tls.caConfigMapName)
#   - the access log from examples/usage-access-log.yaml, the usage collector,
#     and a Gateway-wide ClientTrafficPolicy on nebari-gateway
#   - OpenMeter and the usage console (the openmeter subchart), unless
#     WITH_OPENMETER=0, which removes them and records them as skipped
#
# The provisioning record is written right after the images are loaded, before
# anything is deployed, so it exists whatever happens later. Any failure before
# the final waits exits non-zero. The final waits are bounded: on a timeout
# they print the state and exit 0, so the suite reports what did not converge.
#
# Safe to re-run: only Deployments whose image changed are restarted.
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER_NAME="${CLUSTER_NAME:-llm-serving-test}"
WITH_OPENMETER="${WITH_OPENMETER:-1}"
GATEWAY_HOST_HTTP_PORT="${GATEWAY_HOST_HTTP_PORT:-48080}"
GATEWAY_HOST_HTTPS_PORT="${GATEWAY_HOST_HTTPS_PORT:-48443}"
WAIT_TIMEOUT="${WAIT_TIMEOUT:-300s}"
export CLUSTER_NAME GATEWAY_HOST_HTTP_PORT GATEWAY_HOST_HTTPS_PORT

CONTEXT="kind-${CLUSTER_NAME}"
NODE="${CLUSTER_NAME}-control-plane"
NS=llm-operator-system
GW_NS=envoy-gateway-system
# The openmeter subchart, installed on its own because the dev cluster runs
# the pack from manifests. LLM_OPENMETER_URL in
# manifests/integration/key-manager-patch.yaml names this release and namespace.
OM_RELEASE=usage
OM_NS=usage-openmeter
OM_CHART=../charts/nebari-llm-serving/charts/nebari-openmeter
OPENMETER_URL="http://${OM_RELEASE}-openmeter-api.${OM_NS}.svc"

# Pinned to the kind cluster's context so this never acts on another cluster.
KUBECTL="kubectl --context ${CONTEXT}"
HELM="helm --kube-context ${CONTEXT}"
kustomize_apply() { kubectl kustomize --load-restrictor=LoadRestrictionsNone "$1" | $KUBECTL apply -f - >/dev/null; }

step() { printf '\n==> %s\n' "$*"; }
retry() { # retry N CMD...: run CMD until it succeeds, N attempts 2s apart
  local n="$1"; shift
  for _ in $(seq 1 "$n"); do "$@" && return 0; sleep 2; done
  "$@"
}

case "$WITH_OPENMETER" in
  0|1) ;;
  *) echo "ERROR: WITH_OPENMETER must be 0 or 1, got '$WITH_OPENMETER'" >&2; exit 2 ;;
esac

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# --- 1. cluster ---------------------------------------------------------------
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  step "creating kind cluster $CLUSTER_NAME (make setup)"
  make setup
else
  # kind fixes port mappings at creation: a cluster from the old `make setup`
  # (no dev/kind-config.yaml) has none and must be recreated once.
  mapped="$(docker port "$NODE" 30443/tcp 2>/dev/null || true)"
  if [[ "$mapped" != *"127.0.0.1:${GATEWAY_HOST_HTTPS_PORT}"* ]]; then
    echo "ERROR: cluster $CLUSTER_NAME does not publish the gateway NodePort 30443 on 127.0.0.1:${GATEWAY_HOST_HTTPS_PORT} (found: '${mapped:-none}')." >&2
    echo "       It was created before dev/kind-config.yaml (or with other ports). Recreate it once: make teardown setup" >&2
    exit 1
  fi
  step "using existing kind cluster $CLUSTER_NAME"
fi

# --- 2. images from the working tree --------------------------------------------
# label selector of the pods that run it | image reference
IMAGES=(
  "app=llm-operator|llm-operator:dev"
  "app=llm-key-manager|llm-key-manager:dev"
  "app=mock-provider|mock-vllm:dev"
)
if [[ "$WITH_OPENMETER" == 1 ]]; then
  IMAGES+=(
    "app.kubernetes.io/instance=${OM_RELEASE},app.kubernetes.io/component=console|usage-console:dev"
    "app.kubernetes.io/instance=${OM_RELEASE},app.kubernetes.io/component=bootstrap|openmeter-setup:dev"
  )
fi
step "building and loading images: $(printf '%s ' "${IMAGES[@]#*|}")"
make build-images >/dev/null
make load-images >/dev/null
if [[ "$WITH_OPENMETER" == 1 ]]; then
  docker build -q -f ../openmeter-setup/Dockerfile -t openmeter-setup:dev .. >/dev/null
  docker build -q -f ../usage-console/Dockerfile -t usage-console:dev .. >/dev/null
  kind load docker-image openmeter-setup:dev usage-console:dev --name "$CLUSTER_NAME" >/dev/null
fi

# What the node holds for each image after `kind load`: containerd's id and
# repo digests (a pod's imageID is one of these; Docker's own image ID never is).
for entry in "${IMAGES[@]}"; do
  ref="${entry#*|}"
  docker exec "$NODE" crictl inspecti -o json "$ref" > "$WORK/inspect-${ref%%:*}.json"
done
python3 -I - "$WORK" "${IMAGES[@]}" > "$WORK/images.json" <<'PY'
import json, sys
work, entries = sys.argv[1], sys.argv[2:]
out = {}
for e in entries:
    sel, ref = e.split("|", 1)
    with open(f"{work}/inspect-{ref.split(':')[0]}.json") as f:
        st = json.load(f)["status"]
    out[sel] = {"reference": ref, "id": st["id"], "repo_digests": st.get("repoDigests") or []}
print(json.dumps(out, sort_keys=True))
PY

# --- provisioning record, written before anything is deployed --------------------------
# It exists even when a later step fails (non-zero exit) or does not converge.
SKIPPED=()
[[ "$WITH_OPENMETER" == 1 ]] || SKIPPED+=("openmeter")
python3 -I - "$WORK/images.json" "${SKIPPED[@]}" > "$WORK/record.json" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    images = json.load(f)
print(json.dumps({"mode": "working-tree", "nic_tag": "", "deviations": [],
                  "skipped": sys.argv[2:], "images": images}))
PY
./write-provision-record.sh "$CONTEXT" "$WORK/record.json"

# restart_if_stale NS DEPLOYMENT SELECTOR: restart a Deployment when one of its
# running pods runs an older build of the image recorded for SELECTOR (same
# tag, so the Deployment spec did not change). The containers are found by the
# image their spec names: a status `image` can be a bare sha256 once the tag
# has moved to a newer build. Pods still being created have no imageID yet and
# are skipped.
restart_if_stale() {
  local ns="$1" deploy="$2" sel="$3" stale
  stale="$($KUBECTL -n "$ns" get pods -l "$sel" -o json | python3 -I -c '
import json, sys
rec = json.load(open(sys.argv[1]))[sys.argv[2]]
ref = rec["reference"]
ok = {rec["id"]} | {d.split("@", 1)[1] for d in rec["repo_digests"] if "@" in d}
for p in json.load(sys.stdin)["items"]:
    if p["metadata"].get("deletionTimestamp"):
        continue
    names = {c["name"] for c in p["spec"]["containers"] if c["image"].split("/")[-1] == ref}
    for c in p.get("status", {}).get("containerStatuses", []):
        if c["name"] not in names or not c.get("imageID"):
            continue
        if c["imageID"].split("@")[-1] not in ok:
            print(p["metadata"]["name"])
            break
' "$WORK/images.json" "$sel")"
  if [[ -n "$stale" ]]; then
    echo "    $deploy runs an older build of its image ($(echo "$stale" | tr '\n' ' ')); restarting"
    $KUBECTL -n "$ns" rollout restart "deploy/$deploy" >/dev/null
    $KUBECTL -n "$ns" rollout status "deploy/$deploy" --timeout=180s >/dev/null
  fi
}

# --- 3. Keycloak, operator and key-manager (the integration overlay) ----------
step "applying the profile: Keycloak, operator and key-manager (manifests/integration)"
# What `make deploy-keycloak` and `make deploy-operator` apply besides the
# Deployments the overlay carries.
$KUBECTL create namespace keycloak --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
$KUBECTL create configmap keycloak-realm-import -n keycloak \
  --from-file=realm-nebari.json=keycloak/realm-nebari.json \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
kustomize_apply manifests/integration
$KUBECTL apply -f manifests/cert-manager-config.yaml >/dev/null
$KUBECTL apply -f manifests/webhook.yaml >/dev/null
$KUBECTL -n keycloak rollout status deploy/keycloak --timeout=240s >/dev/null
$KUBECTL -n "$NS" rollout status deploy/llm-operator --timeout=180s >/dev/null
$KUBECTL -n "$NS" rollout status deploy/llm-key-manager --timeout=180s >/dev/null
restart_if_stale "$NS" llm-operator app=llm-operator
restart_if_stale "$NS" llm-key-manager app=llm-key-manager

step "making sure alice (team-a) and bob (team-b) exist in the dev realm"
./demo-users.sh

# --- 4. mock provider behind a PassthroughModel ---------------------------------
step "deploying the mock provider, its PassthroughModel and the CA ConfigMap mock-provider-ca"
if ! $KUBECTL -n "$NS" get secret mock-provider-api-key >/dev/null 2>&1; then
  # The gateway sends this upstream; the mock does not check it.
  openssl rand -hex 24 | tr -d '\n' | $KUBECTL -n "$NS" create secret generic mock-provider-api-key \
    --from-file=apiKey=/dev/stdin >/dev/null
fi
# The operator's webhook may not be serving the instant its rollout returns.
retry 30 $KUBECTL apply -f manifests/usage-mock-provider.yaml >/dev/null
$KUBECTL -n "$NS" wait certificate/mock-provider-tls --for=condition=Ready --timeout=120s >/dev/null
# Public certificate only (self-signed, so it is its own CA); the key stays in the Secret.
$KUBECTL -n "$NS" get secret mock-provider-tls -o jsonpath='{.data.tls\.crt}' | base64 -d \
  | $KUBECTL -n "$NS" create configmap mock-provider-ca --from-file=ca.crt=/dev/stdin \
      --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
restart_if_stale "$NS" mock-provider app=mock-provider

# --- 5. gateway: access log, ClientTrafficPolicy, usage collector ------------------
step "configuring the gateway: access log (manifests/access-log) and ClientTrafficPolicy"
# Written whole (create, or replace when present) rather than applied, so no
# field outside the published example survives, such as the trust-store patch
# usage-e2e.sh makes with `kubectl patch`.
kubectl kustomize --load-restrictor=LoadRestrictionsNone manifests/access-log > "$WORK/envoyproxy.yaml"
if $KUBECTL -n "$GW_NS" get envoyproxy nebari-gateway-proxy >/dev/null 2>&1; then
  $KUBECTL replace -f "$WORK/envoyproxy.yaml" >/dev/null
else
  $KUBECTL create -f "$WORK/envoyproxy.yaml" >/dev/null
fi
$KUBECTL apply -f manifests/gateway-client-traffic-policy.yaml >/dev/null

step "deploying the usage collector from the chart"
mkdir -p "$WORK/collector"
$HELM template dev ../charts/nebari-llm-serving -n "$NS" -s templates/usage-collector.yaml \
  --set fullnameOverride=llm-operator --set global.namespace="$NS" --set platform.baseDomain=local \
  --set auth.oidc.issuerURL=unused --set keyManager.nebariApp.enabled=false --set frontend.enabled=false \
  --set usage.enabled=true --set usage.openmeter.url="$OPENMETER_URL" \
  --set usage.collector.networkPolicy.enabled=false > "$WORK/collector/collector.yaml"
cat > "$WORK/collector/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [collector.yaml]
patches: [{path: $PWD/manifests/usage-collector-dev-patch.yaml}]
EOF
kustomize_apply "$WORK/collector"

# --- 6. OpenMeter and the usage console ------------------------------------------------
if [[ "$WITH_OPENMETER" == 1 ]]; then
  step "installing the openmeter subchart as $OM_RELEASE in $OM_NS (CloudNativePG first)"
  make deploy-cnpg >/dev/null
  # No --wait: the bounded waits below cover the rollouts. Helm still waits
  # for the bootstrap hook Job, which needs OpenMeter's API up.
  $HELM upgrade --install "$OM_RELEASE" "$OM_CHART" -n "$OM_NS" --create-namespace \
    -f manifests/usage-openmeter-values.yaml --timeout 10m >/dev/null
  # The admission a chart install (openmeter.enabled) renders for the
  # collector and key-manager in the operator namespace, from the chart's template.
  $HELM template "$OM_RELEASE" ../charts/nebari-llm-serving -n "$OM_NS" -s templates/openmeter-networkpolicy.yaml \
    --set fullnameOverride=llm-operator --set global.namespace="$NS" --set platform.baseDomain=local \
    --set auth.oidc.issuerURL=unused --set keyManager.nebariApp.enabled=false --set frontend.enabled=false \
    --set usage.enabled=true --set openmeter.enabled=true --set openmeter.namespace="$OM_NS" \
    --set openmeter.setup.image.tag=dev --set openmeter.console.image.tag=dev --set openmeter.console.enabled=false \
    | $KUBECTL apply -f - >/dev/null
  restart_if_stale "$OM_NS" "${OM_RELEASE}-openmeter-console" \
    "app.kubernetes.io/instance=${OM_RELEASE},app.kubernetes.io/component=console"
else
  if $HELM -n "$OM_NS" status "$OM_RELEASE" >/dev/null 2>&1; then
    step "WITH_OPENMETER=0: removing the openmeter subchart ($OM_RELEASE in $OM_NS)"
    $HELM -n "$OM_NS" uninstall "$OM_RELEASE" --wait --timeout 5m >/dev/null
  fi
  if $KUBECTL get namespace "$OM_NS" >/dev/null 2>&1; then
    $KUBECTL delete namespace "$OM_NS" --timeout=5m >/dev/null
  fi
  step "WITH_OPENMETER=0: OpenMeter and the usage console skipped (usage metering is not served)"
fi

# --- 7. bounded convergence waits: report, never fail ----------------------------------
step "waiting (up to $WAIT_TIMEOUT each) for the profile to converge"
PENDING=()
wait_for() { # description CMD...
  local what="$1"; shift
  if "$@" >/dev/null 2>&1; then echo "    ready: $what"; else echo "    NOT ready: $what"; PENDING+=("$what"); fi
}
wait_for "keycloak" $KUBECTL -n keycloak rollout status deploy/keycloak --timeout="$WAIT_TIMEOUT"
for d in llm-operator llm-key-manager mock-provider llm-operator-usage-collector; do
  wait_for "$d" $KUBECTL -n "$NS" rollout status "deploy/$d" --timeout="$WAIT_TIMEOUT"
done
if [[ "$WITH_OPENMETER" == 1 ]]; then
  for w in $($KUBECTL -n "$OM_NS" get deploy,statefulset -o name); do
    wait_for "$OM_NS/$w" $KUBECTL -n "$OM_NS" rollout status "$w" --timeout="$WAIT_TIMEOUT"
  done
fi
wait_for "gateway nebari-gateway Programmed" \
  $KUBECTL -n "$GW_NS" wait gateway/nebari-gateway --for=condition=Programmed --timeout="$WAIT_TIMEOUT"
wait_for "passthroughmodel/mock-provider Ready" \
  $KUBECTL -n "$NS" wait passthroughmodel/mock-provider --for=jsonpath='{.status.phase}'=Ready --timeout="$WAIT_TIMEOUT"

if [[ ${#PENDING[@]} -gt 0 ]]; then
  step "the profile did not converge within $WAIT_TIMEOUT: ${PENDING[*]}"
  $KUBECTL get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded || true
  $KUBECTL -n "$NS" get passthroughmodel,llmmodel || true
  $KUBECTL -n "$GW_NS" get gateway || true
  echo "exiting 0: provisioning finished; the suite reports what is not ready (Q70)"
else
  step "kind-dev profile up on $CONTEXT; gateway on https://<host>:${GATEWAY_HOST_HTTPS_PORT} (127.0.0.1); record: llm-journeys/provision"
fi
