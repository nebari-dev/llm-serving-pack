"""The per-feature report: JUnit files in, a markdown table and an exit code out.

    python -m llm_journeys.report --junit PATH [--junit PATH ...] --features PATH
        --pins PATH --run-info PATH --tier integration|journey|release
        [--feature F10,F12] [--strict-known-failures] [--out PATH]

Exit 0 when every reported feature passed or is a known failure; 1 when one
failed or was not run, or, with --strict-known-failures, when a listed known
failure passed; 2 on a usage error (Q63). Callers run it whatever pytest
returned (Q71).

Each JUnit <testcase> carries the properties the collection hook writes:
nodeid, feature, tiers and, on release tests, stage. A testcase without a
feature property is not a journey test and is ignored.

run-info.json keys read here: target (required), kube_context, nic_tag,
before_nic_tag (after a release run), helper {tag, commit}, components
{name: version} and deviations (a list). Only target is required.

This module imports nothing that needs a cluster or a browser.
"""

import argparse
import json
import re
import sys
import xml.etree.ElementTree as ET
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Literal, get_args

from llm_journeys.features import (
    Feature,
    KnownFailure,
    ReleaseStage,
    Tier,
    load_features,
    load_known_failures,
)

Outcome = Literal["passed", "failed", "not run", "known failure"]

_STAGES = frozenset(get_args(ReleaseStage))
_TIERS = get_args(Tier)
_MESSAGE_LIMIT = 300
_PIN_LINE = re.compile(r"[A-Z_][A-Z0-9_]*=[^\s\"'$#\\`]+")


@dataclass(frozen=True)
class TestResult:
    __test__ = False  # not a pytest test class

    node_id: str  # from the nodeid property; what known_failures matches on
    feature: str
    stage: ReleaseStage | None  # set on release tests (Q59)
    outcome: Literal["passed", "failed", "error", "skipped"]
    message: str = ""  # first line of the <failure> or <error>, bounded (Q69)


@dataclass(frozen=True)
class FeatureResult:
    feature: Feature
    outcome: Outcome  # "skipped" and "error" in JUnit both map to "failed"
    failing_tests: tuple[str, ...]  # unlisted failures
    known_failing_tests: tuple[str, ...]  # listed for this target and failing
    known_passing_tests: tuple[str, ...]  # listed for this target and passing (Q63)
    issues: tuple[str, ...]
    messages: tuple[str, ...] = ()  # one per failing_tests entry, aligned with it (Q69)


def _case_outcome(case: ET.Element) -> str:
    for tag, outcome in (("failure", "failed"), ("error", "error"), ("skipped", "skipped")):
        if case.find(tag) is not None:
            return outcome
    return "passed"


def _case_message(case: ET.Element) -> str:
    """The first line of the failure or error, from its message attribute or else its text."""
    for tag in ("failure", "error"):
        element = case.find(tag)
        if element is not None:
            raw = element.get("message") or element.text or ""
            lines = raw.strip().splitlines()
            first = lines[0].strip() if lines else ""
            return first if len(first) <= _MESSAGE_LIMIT else first[: _MESSAGE_LIMIT - 3] + "..."
    return ""


def read_junit(paths: Sequence[Path]) -> list[TestResult]:
    """Every journey testcase in every file, in order; duplicates are kept (Q59)."""
    results: list[TestResult] = []
    for path in paths:
        for case in ET.parse(path).getroot().iter("testcase"):
            props = {p.get("name"): p.get("value") for p in case.iterfind("properties/property")}
            if "feature" not in props:
                continue
            where = f"{path}: testcase {case.get('classname')}.{case.get('name')}"
            if not props.get("nodeid"):
                raise ValueError(f"{where} has a feature property but no nodeid property")
            stage = props.get("stage")
            if stage is not None and stage not in _STAGES:
                raise ValueError(f"{where} has stage {stage!r}; expected one of {sorted(_STAGES)}")
            results.append(
                TestResult(
                    node_id=props["nodeid"],
                    feature=props["feature"],
                    stage=stage,
                    outcome=_case_outcome(case),
                    message=_case_message(case),
                )
            )
    return results


def read_pins(path: Path) -> dict[str, str]:
    """versions.env: KEY=value per line, # comments, no quoting, no spaces around =."""
    pins: dict[str, str] = {}
    for lineno, line in enumerate(Path(path).read_text().splitlines(), start=1):
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if not _PIN_LINE.fullmatch(line):
            raise ValueError(
                f"{path}:{lineno}: malformed line {line!r}; expected KEY=value with no quotes, "
                "no spaces and no trailing comment"
            )
        key, _, value = line.partition("=")
        if key in pins:
            raise ValueError(f"{path}:{lineno}: {key} is set twice")
        pins[key] = value
    return pins


def feature_results(
    features: Mapping[str, Feature],
    known: Sequence[KnownFailure],
    results: Sequence[TestResult],
    tiers: frozenset[Tier],
    target: str,
    only: frozenset[str] | None = None,
) -> list[FeatureResult]:
    # An entry with no targets applies to every target its test runs on, and
    # every test in these files ran on this run's target.
    listed = {k.test: k for k in known if not k.targets or target in k.targets}

    out: list[FeatureResult] = []
    for feature_id, feature in features.items():
        if not tiers & set(feature.tiers) or (only is not None and feature_id not in only):
            continue
        mine = [r for r in results if r.feature == feature_id]
        # node ID -> did every result for it pass, in first-seen order
        passed_by_test: dict[str, bool] = {}
        message_by_test: dict[str, str] = {}  # the first non-empty message per test
        for r in mine:
            passed_by_test[r.node_id] = passed_by_test.get(r.node_id, True) and r.outcome == "passed"
            if r.message and not message_by_test.get(r.node_id):
                message_by_test[r.node_id] = r.message

        failing = tuple(t for t, ok in passed_by_test.items() if not ok and t not in listed)
        known_failing = tuple(t for t, ok in passed_by_test.items() if not ok and t in listed)
        known_passing = tuple(t for t, ok in passed_by_test.items() if ok and t in listed)
        issues = tuple(dict.fromkeys(listed[t].issue for t in known_failing))

        missing_stage = False
        if "release" in tiers and "release" in feature.tiers:
            missing_stage = not _STAGES <= {r.stage for r in mine}

        outcome: Outcome
        if failing:
            outcome = "failed"
        elif not mine or missing_stage:  # before known failures: one stage alone never exits 0
            outcome = "not run"
        elif known_failing:
            outcome = "known failure"
        else:
            outcome = "passed"
        messages = tuple(message_by_test.get(t, "") for t in failing)
        out.append(FeatureResult(feature, outcome, failing, known_failing, known_passing, issues, messages))
    return out


def exit_code(results: Sequence[FeatureResult], *, strict_known_failures: bool) -> int:
    if any(r.outcome in ("failed", "not run") for r in results):
        return 1
    if strict_known_failures and any(r.known_passing_tests for r in results):
        return 1
    return 0


def _cell(text: str) -> str:
    return text.replace("|", "\\|").replace("\n", " ")


def _outcome_text(result: FeatureResult) -> str:
    if result.outcome == "known failure":
        return f"known failure ({', '.join(result.issues)})"
    return result.outcome


def _notes(result: FeatureResult) -> str:
    notes: list[str] = []
    if result.failing_tests:
        notes.append(
            "failing: "
            + ", ".join(
                f"{test} ({message})" if message else test
                for test, message in zip(result.failing_tests, result.messages, strict=False)
            )
        )
    if result.known_failing_tests:
        notes.append("known failing: " + ", ".join(result.known_failing_tests))
    if result.known_passing_tests:
        notes.append(
            "listed in known_failures but passed, remove the entry: " + ", ".join(result.known_passing_tests)
        )
    if result.outcome == "not run":
        if "release" in result.feature.tiers:
            notes.append("needs a result from both the before and the after stage")
        else:
            notes.append("no result in this run")
    return "; ".join(notes)


def render_markdown(results: Sequence[FeatureResult], pins: Mapping[str, str], run_info: Mapping[str, object]) -> str:
    lines = [f"# Journey report: {run_info.get('target')}", ""]
    lines += ["| Feature | Title | Outcome | Notes |", "|---|---|---|---|"]
    for r in results:
        lines.append(f"| {r.feature.id} | {_cell(r.feature.title)} | {_outcome_text(r)} | {_cell(_notes(r))} |")

    lines += ["", "## Run", ""]
    lines.append(f"- Target: {run_info.get('target')}")
    if run_info.get("kube_context"):
        lines.append(f"- Kube context: {run_info['kube_context']}")
    nic = run_info.get("nic_tag") or "none"
    if run_info.get("before_nic_tag"):
        nic += f" (before stage: {run_info['before_nic_tag']})"
    lines.append(f"- NIC tag: {nic}")
    helper = run_info.get("helper")
    if isinstance(helper, Mapping):
        lines.append(f"- nebari_journeys: tag {helper.get('tag')}, commit {helper.get('commit')}")
    components = run_info.get("components")
    if isinstance(components, Mapping):
        for name, version in components.items():
            lines.append(f"- {name} (installed): {version}")

    lines += ["", "## Pins (versions.env)", ""]
    lines += [f"- {key}: {value}" for key, value in pins.items()] or ["- none"]

    lines += ["", "## Deviations", ""]
    deviations = run_info.get("deviations") or []
    lines += [f"- {d}" for d in deviations] or ["- none"]  # type: ignore[union-attr]
    return "\n".join(lines) + "\n"


class _UsageError(Exception):
    pass


class _Parser(argparse.ArgumentParser):
    def error(self, message: str):  # type: ignore[override]
        raise _UsageError(f"{self.prog}: {message}")


def _parser() -> _Parser:
    p = _Parser(prog="python -m llm_journeys.report", description="Per-feature report from journey JUnit files.")
    p.add_argument("--junit", action="append", required=True, type=Path, help="JUnit XML file; repeat for several")
    p.add_argument("--features", required=True, type=Path, help="features.toml")
    p.add_argument("--pins", required=True, type=Path, help="versions.env")
    p.add_argument("--run-info", required=True, type=Path, help="run-info.json")
    p.add_argument("--tier", required=True, choices=_TIERS)
    p.add_argument("--feature", default=None, help="Comma-separated feature IDs to report, such as F10,F12")
    p.add_argument("--strict-known-failures", action="store_true", help="Exit 1 when a listed known failure passes")
    p.add_argument("--out", type=Path, default=None, help="Write the report here instead of stdout")
    return p


def _only(raw: str | None, features: Mapping[str, Feature], tier: str) -> frozenset[str] | None:
    if raw is None:
        return None
    wanted = frozenset(part.strip() for part in raw.split(",") if part.strip())
    if not wanted:
        raise _UsageError("--feature names no feature ID")
    for feature_id in sorted(wanted):
        if feature_id not in features:
            raise _UsageError(f"--feature {feature_id} is not in features.toml")
        if tier not in features[feature_id].tiers:
            raise _UsageError(f"--feature {feature_id} is not in the {tier} tier")
    return wanted


def main(argv: Sequence[str]) -> int:
    try:
        args = _parser().parse_args(list(argv))
        features = load_features(args.features)
        known = load_known_failures(args.features)
        only = _only(args.feature, features, args.tier)
        pins = read_pins(args.pins)
        run_info = json.loads(args.run_info.read_text())
        if not isinstance(run_info, dict) or not isinstance(run_info.get("target"), str) or not run_info["target"]:
            raise _UsageError(f"{args.run_info}: has no target")
        results = read_junit(args.junit)
    except (_UsageError, OSError, ValueError, KeyError, TypeError, ET.ParseError) as err:
        # tomllib.TOMLDecodeError and json.JSONDecodeError are ValueErrors.
        print(f"llm_journeys.report: {err}", file=sys.stderr)
        return 2

    reported = feature_results(features, known, results, frozenset({args.tier}), run_info["target"], only)
    code = exit_code(reported, strict_known_failures=args.strict_known_failures)
    text = render_markdown(reported, pins, run_info)
    if not args.strict_known_failures:
        for r in reported:
            for test in r.known_passing_tests:
                text += f"Warning: {test} is listed in known_failures but passed; remove its entry.\n"
    if args.out is not None:
        args.out.write_text(text)
    else:
        sys.stdout.write(text)
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
