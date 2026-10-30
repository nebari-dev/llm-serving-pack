#!/usr/bin/env bash
# check-examples-render.sh - Render every example's chart values against the
# chart in this checkout.
#
# Finds each Argo CD Application source under examples/ that installs this
# chart (chart nebari-llm-serving, or a path ending charts/nebari-llm-serving)
# with helm.values, renders the local chart with those values and the source's
# releaseName, and checks that the pack's namespace matches the Application's
# destination.namespace and that a pinned chart version matches Chart.yaml.
# A failed render means a reader who copies the example gets a failed sync.
#
# Needs helm and yq (mikefarah v4). Exits 0 on success; exits 1 and lists
# offenders on failure.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$REPO_ROOT/charts/nebari-llm-serving"
VERSION="$(yq '.version' "$CHART/Chart.yaml")"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failed=0
checked=0

while IFS= read -r -d '' file; do
    rel="${file#"$REPO_ROOT"/}"
    # One line per source of this chart with helm values: <doc>\t<source>.
    if ! pairs="$(yq eval-all -o=tsv '
        select(.kind == "Application") | . as $app | $app.spec.sources // [$app.spec.source]
        | to_entries | .[]
        | select(.value.chart == "nebari-llm-serving" or ((.value.path // "") | test("charts/nebari-llm-serving/?$")))
        | select(.value.helm.values != null)
        | [document_index, .key]' "$file" 2> "$WORK/err.txt")"; then
        echo "FAIL $rel: yq could not read the file:" >&2
        sed 's/^/    /' "$WORK/err.txt" >&2
        failed=1
        continue
    fi
    [ -n "$pairs" ] || continue

    while IFS=$'\t' read -r doc idx; do
        checked=$((checked + 1))
        label="$rel (document $doc, source $idx)"
        q="select(document_index == $doc) | (.spec.sources // [.spec.source])[$idx]"
        yq eval-all "$q | .helm.values" "$file" > "$WORK/values.yaml"
        release="$(yq eval-all "$q | .helm.releaseName // \"\"" "$file")"
        [ -n "$release" ] || release="$(yq eval-all "select(document_index == $doc) | .metadata.name" "$file")"
        dest="$(yq eval-all "select(document_index == $doc) | .spec.destination.namespace // \"\"" "$file")"
        pin="$(yq eval-all "$q | select(.chart != null) | .targetRevision // \"\"" "$file")"

        if [ -n "$pin" ] && [ "$pin" != "$VERSION" ]; then
            echo "FAIL $label: pins chart $pin but Chart.yaml is $VERSION" >&2
            failed=1
            continue
        fi

        if ! helm template "$release" "$CHART" --namespace "$dest" \
                -f "$WORK/values.yaml" > "$WORK/out.yaml" 2> "$WORK/err.txt"; then
            echo "FAIL $label: helm template failed:" >&2
            sed 's/^/    /' "$WORK/err.txt" >&2
            failed=1
            continue
        fi

        # The pack's namespace is the namespace of the operator's Deployment.
        ns="$(yq eval-all 'select(.kind == "Deployment" and .metadata.name == "*operator*") | .metadata.namespace' "$WORK/out.yaml" | head -n1)"
        if [ "$ns" != "$dest" ]; then
            echo "FAIL $label: chart renders the pack into '$ns' but destination.namespace is '$dest'" >&2
            failed=1
            continue
        fi
        echo "ok   $label"
    done <<< "$pairs"
done < <(find "$REPO_ROOT/examples" -name '*.yaml' -print0 | sort -z)

if [ "$checked" -eq 0 ]; then
    echo "ERROR: found no example with chart values - the yq query is likely stale." >&2
    exit 1
fi
if [ "$failed" -ne 0 ]; then
    exit 1
fi
echo "EXAMPLES_OK ($checked rendered)"
