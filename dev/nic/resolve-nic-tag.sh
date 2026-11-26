#!/usr/bin/env bash
# Print the NIC release tag to use, once per run (Q6, Q73).
#
#   dev/nic/resolve-nic-tag.sh
#
# Prints $NIC_VERSION when it is set, and the word "override" on stderr so a
# caller can record the pinned run as a deviation. Otherwise prints the latest
# release tag of nebari-dev/nebari-infrastructure-core from `gh release view`.
# Either way the tag must look like vX.Y.Z or vX.Y.Z-rc.N, or the script fails.
set -euo pipefail

if [ -n "${NIC_VERSION:-}" ]; then
  tag="$NIC_VERSION"
  echo "override" >&2
else
  tag="$(gh release view --repo nebari-dev/nebari-infrastructure-core --json tagName -q .tagName)"
fi

if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
  echo "resolve-nic-tag.sh: '$tag' is not a NIC release tag (vX.Y.Z or vX.Y.Z-rc.N)" >&2
  exit 1
fi
printf '%s\n' "$tag"
