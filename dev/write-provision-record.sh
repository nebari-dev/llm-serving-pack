#!/usr/bin/env bash
# Write the provisioning record: the ConfigMap `provision` in the namespace
# `llm-journeys`, which provisioning creates and owns. The journey suite reads
# it when it opens the target.
#
#   write-provision-record.sh <kube-context> <json-file>
#
# <json-file> holds one object with the keys
#   mode        string, "working-tree" or "release:<version>"
#   nic_tag     string, empty on kind-dev
#   deviations  list of strings
#   skipped     list of strings
#   images      object: pod label selector -> {"reference", "id", "repo_digests"}
# Strings are stored as they are; lists and objects as JSON text. The namespace
# is created if absent, and the ConfigMap is replaced whole, so no key from an
# earlier record survives.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <kube-context> <json-file>" >&2
  exit 2
fi
CONTEXT="$1"
JSON_FILE="$2"
NS=llm-journeys
KUBECTL="kubectl --context ${CONTEXT}"

manifest="$(python3 -I - "$JSON_FILE" "$NS" <<'PY'
import json, sys
path, ns = sys.argv[1], sys.argv[2]
with open(path) as f:
    rec = json.load(f)
want = {"mode": str, "nic_tag": str, "deviations": list, "skipped": list, "images": dict}
missing = sorted(set(want) - set(rec))
extra = sorted(set(rec) - set(want))
if missing or extra:
    sys.exit(f"record keys: missing {missing}, unexpected {extra}")
for k, t in want.items():
    if not isinstance(rec[k], t):
        sys.exit(f"record key {k}: want {t.__name__}, got {type(rec[k]).__name__}")
for sel, img in rec["images"].items():
    if set(img) != {"reference", "id", "repo_digests"} or not img["id"]:
        sys.exit(f"image {sel}: want reference, id (non-empty) and repo_digests, got {sorted(img)}")
data = {k: (v if isinstance(v, str) else json.dumps(v, sort_keys=True)) for k, v in rec.items()}
print(json.dumps({"apiVersion": "v1", "kind": "ConfigMap",
                  "metadata": {"name": "provision", "namespace": ns}, "data": data}))
PY
)"

$KUBECTL create namespace "$NS" --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
if $KUBECTL -n "$NS" get configmap provision >/dev/null 2>&1; then
  printf '%s\n' "$manifest" | $KUBECTL replace -f - >/dev/null
else
  printf '%s\n' "$manifest" | $KUBECTL create -f - >/dev/null
fi
echo "provisioning record written: $NS/provision (context $CONTEXT)"
