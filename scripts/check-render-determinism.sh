#!/usr/bin/env bash
# Guards the defect class behind issue #84: Helm charts that generate
# certificate material (or any random value) at render time are broken under
# ArgoCD, Flux, and every other cluster-less renderer, because `lookup` returns
# empty and the guarded branch is never taken.
#
# Keep CHART_VERSION and VALUES in sync with examples/envoy-ai-gateway.yaml.
# The docs test suite enforces that; it is not left to reviewer diligence.
set -euo pipefail

CHART="oci://docker.io/envoyproxy/ai-gateway-helm"
CHART_VERSION="v0.5.0"
NAMESPACE="envoy-ai-gateway-system"
RELEASE="envoy-ai-gateway"
CACHE_DIR="${CHART_CACHE_DIR:-.chart-cache}"

VALUES='controller:
  mutatingWebhook:
    certManager:
      enable: true
'

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf '%s' "$VALUES" > "$work/values.yaml"

mkdir -p "$CACHE_DIR"
chart_dir="$CACHE_DIR/ai-gateway-helm-$CHART_VERSION"
if [ ! -d "$chart_dir" ]; then
  echo "Pulling $CHART $CHART_VERSION"
  mkdir -p "$chart_dir"
  helm pull "$CHART" --version "$CHART_VERSION" -d "$chart_dir" --untar
else
  echo "Using cached chart at $chart_dir"
fi
chart_path="$chart_dir/ai-gateway-helm"

for i in 1 2; do
  helm template "$RELEASE" "$chart_path" \
    --namespace "$NAMESPACE" -f "$work/values.yaml" > "$work/render-$i.yaml"
done

fail=0

if ! diff -q "$work/render-1.yaml" "$work/render-2.yaml" >/dev/null; then
  echo "FAIL: two renders of the same chart+values differ."
  echo "      The chart generates non-deterministic material at render time."
  diff "$work/render-1.yaml" "$work/render-2.yaml" | head -20 || true
  fail=1
else
  echo "PASS: renders are byte-identical."
fi

if grep -q "tls\.key:" "$work/render-1.yaml"; then
  echo "FAIL: the render contains private key material."
  echo "      Certificate material must never travel through the GitOps render."
  fail=1
else
  echo "PASS: no private key material in the render."
fi

if grep -q "cert-manager.io/inject-ca-from" "$work/render-1.yaml"; then
  echo "PASS: MutatingWebhookConfiguration delegates caBundle to cainjector."
else
  echo "FAIL: no inject-ca-from annotation; nothing maintains caBundle."
  fail=1
fi

exit $fail
