#!/usr/bin/env bash
# Tests for tests/journeys/install-helpers.sh's tag and commit checks, with
# stub git and pixi commands on PATH. Needs no network and no pixi. The script
# is copied into a scratch directory so its results/helpers.json lands there,
# not in the real results/.
#
#   bash tests/journeys/tests_lib/install-helpers.test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$(cd "$here/.." && pwd)/install-helpers.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

stubs="$work/bin"
mkdir -p "$stubs"

# git: `git ls-remote` prints $STUB_LS_REMOTE, exits $STUB_GIT_CODE (default 0).
cat >"$stubs/git" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$STUB_LOG/git.args"
[ -z "${STUB_LS_REMOTE:-}" ] || printf '%b\n' "$STUB_LS_REMOTE"
exit "${STUB_GIT_CODE:-0}"
STUB
# pixi: records its arguments.
cat >"$stubs/pixi" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$STUB_LOG/pixi.args"
STUB
chmod +x "$stubs/git" "$stubs/pixi"

sha="dfae9f2ea2e89d0ccab464f7e7d5285e6defec59"
peeled="0123456789abcdef0123456789abcdef01234567"
failures=0
n=0

fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# run NAME TAG LS_REMOTE GIT_CODE: sets $code and $dir (the scratch suite dir).
run() {
  n=$((n + 1))
  dir="$work/case-$n"
  mkdir -p "$dir/log"
  cp "$script" "$dir/install-helpers.sh"
  code=0
  PATH="$stubs:$PATH" STUB_LOG="$dir/log" STUB_LS_REMOTE="$3" STUB_GIT_CODE="$4" \
    "$dir/install-helpers.sh" "$2" >"$dir/log/out" 2>&1 || code=$?
}

# A tag that fails the pattern: exit 2, nothing queried or installed.
run bad-tag 'v0.18.0 --index-url x' "" 0
[ "$code" = 2 ] || fail "bad tag: exit $code, want 2"
[ ! -f "$dir/log/git.args" ] || fail "bad tag: git ran"
[ ! -f "$dir/log/pixi.args" ] || fail "bad tag: pixi ran"

# A tag that resolves to nothing, or to something that is not a 40-hex SHA,
# or whose lookup fails: exit 1 naming the tag, nothing installed or written.
for case in "|0" "not-a-sha\trefs/tags/v0.18.0|0" "${sha:0:39}\trefs/tags/v0.18.0|0" "${sha^^}\trefs/tags/v0.18.0|0" "|128"; do
  run unresolved v0.18.0 "${case%|*}" "${case##*|}"
  [ "$code" = 1 ] || fail "ls-remote '${case}': exit $code, want 1"
  grep -q "v0.18.0" "$dir/log/out" || fail "ls-remote '${case}': message does not name the tag: $(cat "$dir/log/out")"
  [ ! -f "$dir/log/pixi.args" ] || fail "ls-remote '${case}': pixi ran"
  [ ! -f "$dir/results/helpers.json" ] || fail "ls-remote '${case}': helpers.json written"
done

# A lightweight tag: pip installs the commit, helpers.json records tag and commit.
run lightweight v0.18.0 "$sha\trefs/tags/v0.18.0" 0
[ "$code" = 0 ] || fail "lightweight: exit $code, want 0: $(cat "$dir/log/out")"
case "$(cat "$dir/log/pixi.args" 2>/dev/null)" in
  *"--isolated install --no-deps --force-reinstall git+https://github.com/nebari-dev/nebari-infrastructure-core@$sha#subdirectory=tests/user_journeys") ;;
  *) fail "lightweight: pixi args: $(cat "$dir/log/pixi.args" 2>/dev/null)" ;;
esac
[ "$(cat "$dir/results/helpers.json" 2>/dev/null)" = "{\"tag\": \"v0.18.0\", \"commit\": \"$sha\"}" ] ||
  fail "lightweight: helpers.json: $(cat "$dir/results/helpers.json" 2>/dev/null)"

# An annotated tag: the peeled commit wins over the tag object.
run annotated v0.18.0 "$sha\trefs/tags/v0.18.0\n$peeled\trefs/tags/v0.18.0^{}" 0
[ "$code" = 0 ] || fail "annotated: exit $code, want 0: $(cat "$dir/log/out")"
grep -q "@$peeled#" "$dir/log/pixi.args" 2>/dev/null || fail "annotated: pixi args: $(cat "$dir/log/pixi.args" 2>/dev/null)"
grep -q "\"commit\": \"$peeled\"" "$dir/results/helpers.json" 2>/dev/null || fail "annotated: helpers.json"

if [ "$failures" -ne 0 ]; then
  echo "$failures failure(s)" >&2
  exit 1
fi
echo "install-helpers.test.sh: all passed"
