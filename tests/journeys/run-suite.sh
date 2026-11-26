#!/usr/bin/env bash
# Run the journey suite and its report with the spec's run-status rule (Q71).
#
#   run-suite.sh --tier integration|journey|release [--feature F4,F8]
#       [--strict-known-failures] [--pins PATH] -- <pytest args>
#
# Runs from tests/journeys/ inside the suite environment (pytest and python on
# PATH, as `pixi run` gives them). pytest writes results/junit.xml; its exit
# code is captured, and the report always runs after it, whatever pytest
# returned. The final status is the report's exit code when pytest exited 0
# or 1, and pytest's own code otherwise (2 interrupted, 3 internal error,
# 4 usage error, 5 no tests collected), with the report still run for its
# output. --feature goes to both pytest and the report. The pins file is
# --pins, else $PINS_FILE, else versions.env at the repo root.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/../.." && pwd)"

usage() {
  echo "usage: $0 --tier integration|journey|release [--feature LIST] [--strict-known-failures] [--pins PATH] -- <pytest args>" >&2
  exit 2
}

tier=""
feature=""
strict=0
pins="${PINS_FILE:-$repo_root/versions.env}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tier) [ "$#" -ge 2 ] || usage; tier="$2"; shift 2 ;;
    --feature) [ "$#" -ge 2 ] || usage; feature="$2"; shift 2 ;;
    --strict-known-failures) strict=1; shift ;;
    --pins) [ "$#" -ge 2 ] || usage; pins="$2"; shift 2 ;;
    --) shift; break ;;
    *) echo "run-suite.sh: unknown option $1" >&2; usage ;;
  esac
done
case "$tier" in
  integration | journey | release) ;;
  *) usage ;;
esac

cd "$here"
mkdir -p results

pytest_args=(--junitxml=results/junit.xml)
[ -z "$feature" ] || pytest_args+=("--feature=$feature")
pytest_code=0
pytest "${pytest_args[@]}" "$@" || pytest_code=$?

report_args=(
  --junit results/junit.xml
  --features features.toml
  --pins "$pins"
  --run-info results/run-info.json
  --tier "$tier"
)
[ -z "$feature" ] || report_args+=(--feature "$feature")
[ "$strict" = 0 ] || report_args+=(--strict-known-failures)
report_code=0
python -m llm_journeys.report "${report_args[@]}" || report_code=$?

case "$pytest_code" in
  0 | 1) exit "$report_code" ;;
  *)
    echo "run-suite.sh: pytest exited $pytest_code; that is the run's status (report exited $report_code)" >&2
    exit "$pytest_code"
    ;;
esac
