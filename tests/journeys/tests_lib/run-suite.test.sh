#!/usr/bin/env bash
# Tests for tests/journeys/run-suite.sh's run-status rule (Q71), with stub
# pytest and python commands on PATH. Needs no cluster and no pixi.
#
#   bash tests/journeys/tests_lib/run-suite.test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
suite="$(cd "$here/.." && pwd)/run-suite.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

stubs="$work/bin"
mkdir -p "$stubs"

# pytest: records its arguments, exits with $STUB_PYTEST_CODE.
cat >"$stubs/pytest" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$STUB_LOG/pytest.args"
exit "$STUB_PYTEST_CODE"
EOF

# python: only `python -m llm_journeys.report ...` is expected. Records that it
# ran and its arguments, exits with $STUB_REPORT_CODE.
cat >"$stubs/python" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" != "-m" ] || [ "${2:-}" != "llm_journeys.report" ]; then
  echo "unexpected python call: $*" >&2
  exit 99
fi
shift 2
printf '%s\n' "$*" >"$STUB_LOG/report.args"
exit "$STUB_REPORT_CODE"
EOF
chmod +x "$stubs/pytest" "$stubs/python"

failures=0

fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# check PYTEST_CODE REPORT_CODE EXPECTED
check() {
  local pytest_code="$1" report_code="$2" expected="$3" log code
  log="$work/log-$pytest_code-$report_code"
  mkdir -p "$log"
  code=0
  PATH="$stubs:$PATH" STUB_LOG="$log" STUB_PYTEST_CODE="$pytest_code" STUB_REPORT_CODE="$report_code" \
    PINS_FILE="$work/no-such-versions.env" \
    "$suite" --tier integration --feature F4 --strict-known-failures -- journeys --target=kind-dev \
    >"$log/out" 2>&1 || code=$?

  if [ "$code" != "$expected" ]; then
    fail "(pytest $pytest_code, report $report_code): exit $code, want $expected"
    sed 's/^/    /' "$log/out" >&2
  fi
  if [ ! -f "$log/report.args" ]; then
    fail "(pytest $pytest_code, report $report_code): the report did not run"
    return
  fi
  local pytest_args report_args
  pytest_args="$(cat "$log/pytest.args")"
  report_args="$(cat "$log/report.args")"
  case "$pytest_args" in
    *"--junitxml=results/junit.xml"*"--feature=F4"*"journeys --target=kind-dev"*) ;;
    *) fail "(pytest $pytest_code, report $report_code): pytest args: $pytest_args" ;;
  esac
  case "$report_args" in
    *"--junit results/junit.xml"*"--tier integration"*"--feature F4"*"--strict-known-failures"*) ;;
    *) fail "(pytest $pytest_code, report $report_code): report args: $report_args" ;;
  esac
  case "$report_args" in
    *"--pins $work/no-such-versions.env"*) ;;
    *) fail "(pytest $pytest_code, report $report_code): report not given PINS_FILE: $report_args" ;;
  esac
}

# (pytest, report) -> final status, per the spec's Run status (Q71).
check 0 0 0
check 1 0 0
check 1 1 1
check 2 0 2
check 5 0 5

# Usage errors of run-suite.sh itself exit 2 and run nothing.
usage_log="$work/usage"
mkdir -p "$usage_log"
code=0
PATH="$stubs:$PATH" STUB_LOG="$usage_log" STUB_PYTEST_CODE=0 STUB_REPORT_CODE=0 \
  "$suite" -- journeys >/dev/null 2>&1 || code=$?
[ "$code" = 2 ] || fail "missing --tier: exit $code, want 2"
[ ! -f "$usage_log/pytest.args" ] || fail "missing --tier: pytest ran"

if [ "$failures" -ne 0 ]; then
  echo "$failures failure(s)" >&2
  exit 1
fi
echo "run-suite.test.sh: all passed"
