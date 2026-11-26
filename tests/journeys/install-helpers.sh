#!/usr/bin/env bash
# Install nebari_journeys from the NIC git repository at a release tag into
# this suite's pixi environment, outside pixi.lock (Q46, Q89).
#
#   tests/journeys/install-helpers.sh <tag>      # tag from dev/nic/resolve-nic-tag.sh
#
# The package version is 0.1.0 at every tag, so the tag and the commit it
# points at are what identify it. The tag is resolved to its commit with
# `git ls-remote` first and pip installs that commit, so what is installed is
# exactly what results/helpers.json records, even if the tag moves. Only
# that commit reaches pip from this script: --isolated ignores PIP_*
# variables and pip config files, and --no-deps keeps pip off the packages
# the lock pins. --force-reinstall replaces a helper installed from another
# tag, which pip would otherwise keep because the version is the same.
set -euo pipefail

repo="https://github.com/nebari-dev/nebari-infrastructure-core"

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <tag>" >&2
  exit 2
fi
tag="$1"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
  echo "install-helpers.sh: '$tag' is not a NIC release tag (vX.Y.Z or vX.Y.Z-rc.N)" >&2
  exit 2
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The commit the tag points at; the peeled ^{} line wins for an annotated tag.
refs="$(git ls-remote "$repo" "refs/tags/$tag" "refs/tags/$tag^{}")" || {
  echo "install-helpers.sh: could not resolve tag $tag: git ls-remote $repo failed" >&2
  exit 1
}
peeled="$(printf '%s\n' "$refs" | awk -v ref="refs/tags/$tag^{}" '$2 == ref { print $1 }')"
plain="$(printf '%s\n' "$refs" | awk -v ref="refs/tags/$tag" '$2 == ref { print $1 }')"
commit="${peeled:-$plain}"
if ! [[ "$commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "install-helpers.sh: tag $tag did not resolve to a commit in $repo" >&2
  exit 1
fi

pixi run --manifest-path "$here/pixi.toml" --locked \
  pip --isolated install --no-deps --force-reinstall \
  "git+$repo@$commit#subdirectory=tests/user_journeys"

mkdir -p "$here/results"
printf '{"tag": "%s", "commit": "%s"}\n' "$tag" "$commit" >"$here/results/helpers.json"
echo "nebari_journeys installed from $repo at $tag ($commit)"
