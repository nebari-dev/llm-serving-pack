#!/usr/bin/env bash
# diff-default-render.sh - Diff the chart's default render against a base ref.
#
# Renders charts/nebari-llm-serving at BASE_REF (default origin/main) and in
# this checkout, each with scripts/chart-render-values.yaml and nothing else,
# and prints a unified diff. An optional feature must leave this diff empty
# when it is off, so a non-empty diff means every existing install changes on
# upgrade.
#
# Usage: scripts/diff-default-render.sh [BASE_REF]
# Exits 0 when the renders match, 1 when they differ, 2 when a render fails.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_REF="${1:-origin/main}"
VALUES="$REPO_ROOT/scripts/chart-render-values.yaml"
RELEASE="nebari-llm-serving"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/base"
if ! git -C "$REPO_ROOT" archive "$BASE_REF" charts/nebari-llm-serving | tar -x -C "$WORK/base"; then
    echo "ERROR: could not read charts/nebari-llm-serving at $BASE_REF" >&2
    exit 2
fi

render() {
    helm template "$RELEASE" "$1" --namespace "$RELEASE-system" -f "$VALUES" > "$2"
}

if ! render "$WORK/base/charts/nebari-llm-serving" "$WORK/base.yaml"; then
    echo "ERROR: the chart at $BASE_REF does not render with $VALUES" >&2
    exit 2
fi
if ! render "$REPO_ROOT/charts/nebari-llm-serving" "$WORK/head.yaml"; then
    echo "ERROR: the chart in this checkout does not render with $VALUES" >&2
    exit 2
fi

if diff -u --label "$BASE_REF" --label "this checkout" "$WORK/base.yaml" "$WORK/head.yaml"; then
    echo "DEFAULT_RENDER_UNCHANGED"
    exit 0
fi
exit 1
