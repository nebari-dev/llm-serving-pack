"""Tests for the collection hook in journeys/conftest.py. These need no cluster.

Each test builds a small copy of tests/journeys/ with pytester: the real root
conftest.py, pytest.ini and journeys/conftest.py, plus its own features.toml
and test modules.
"""

import shutil
import textwrap
import xml.etree.ElementTree as ET
from pathlib import Path

import pytest

SUITE = Path(__file__).resolve().parent.parent

# Every feature tier below has a test in FULL_TREE, so an unfiltered run of
# FULL_TREE has no coverage gap.
FEATURES = """
known_failures = []

[F1]
title = "journey only"
tiers = ["journey"]

[F2]
title = "both tiers"
tiers = ["integration", "journey"]

[F8]
title = "integration only"
tiers = ["integration"]

[F11]
title = "broken gateway"
tiers = ["integration"]

[F13]
title = "upgrade"
tiers = ["release"]
"""

FULL_TREE = {
    "journeys/test_install.py": """
        import pytest

        @pytest.mark.feature("F1")
        @pytest.mark.journey
        def test_converges():
            pass
    """,
    "journeys/test_models.py": """
        import pytest

        @pytest.mark.feature("F2")
        @pytest.mark.integration
        @pytest.mark.journey
        def test_model_ready():
            pass
    """,
    "journeys/test_scoping.py": """
        import pytest

        @pytest.mark.feature("F8")
        @pytest.mark.integration
        def test_spoofing_rejected():
            pass
    """,
    "journeys/test_zz_gateway_failure.py": """
        import pytest

        @pytest.mark.feature("F11")
        @pytest.mark.integration
        def test_gateway_failure_heals():
            pass
    """,
    "journeys/test_upgrade.py": """
        import pytest

        @pytest.mark.feature("F13")
        @pytest.mark.release("before")
        def test_upgrade_before():
            pass

        @pytest.mark.feature("F13")
        @pytest.mark.release("after")
        def test_upgrade_after():
            pass
    """,
}

JOURNEY_ONLY_TEST = "journeys/test_install.py::test_converges"
F8_TEST = "journeys/test_scoping.py::test_spoofing_rejected"


def _suite(pytester: pytest.Pytester, tree: dict[str, str], features: str = FEATURES) -> None:
    for name in ("conftest.py", "pytest.ini", "journeys/__init__.py", "journeys/conftest.py"):
        dest = pytester.path / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy(SUITE / name, dest)
    (pytester.path / "features.toml").write_text(textwrap.dedent(features))
    for name, body in tree.items():
        (pytester.path / name).write_text(textwrap.dedent(body))


def _with_known_failure(entry: str) -> str:
    return FEATURES.replace("known_failures = []", f"known_failures = [{entry}]")


def _run(pytester: pytest.Pytester, *args: str) -> pytest.RunResult:
    return pytester.runpytest_subprocess("journeys", "-p", "no:cacheprovider", *args)


def _collected(result: pytest.RunResult) -> list[str]:
    return [line for line in result.outlines if line.startswith("journeys/") and "::" in line]


def _single(tree_body: str) -> dict[str, str]:
    return {"journeys/test_one.py": tree_body}


def _junit_cases(path: Path) -> dict[str, dict]:
    cases = {}
    for case in ET.parse(path).getroot().iter("testcase"):
        props = {p.get("name"): p.get("value") for p in case.iter("property")}
        errors = [e.get("message", "") + (e.text or "") for e in case.iter("error")]
        cases[props.get("nodeid")] = {"props": props, "errors": errors}
    return cases


# Marker rules.


def test_full_tree_collects_cleanly(pytester):
    _suite(pytester, FULL_TREE)
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == 0, result.stdout.str() + result.stderr.str()


def test_missing_feature_marker_fails_collection(pytester):
    tree = dict(FULL_TREE)
    tree["journeys/test_one.py"] = """
        import pytest

        @pytest.mark.journey
        def test_no_feature():
            pass
    """
    _suite(pytester, tree)
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines(["*journeys/test_one.py::test_no_feature*no feature marker*"])


def test_unknown_feature_id_fails(pytester):
    tree = dict(FULL_TREE)
    tree["journeys/test_one.py"] = """
        import pytest

        @pytest.mark.feature("F99")
        @pytest.mark.journey
        def test_unknown():
            pass
    """
    _suite(pytester, tree)
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines(["*journeys/test_one.py::test_unknown*F99*not in features.toml*"])


def test_two_feature_markers_fail(pytester):
    tree = dict(FULL_TREE)
    tree["journeys/test_one.py"] = """
        import pytest

        @pytest.mark.feature("F1")
        @pytest.mark.feature("F2")
        @pytest.mark.journey
        def test_two():
            pass
    """
    _suite(pytester, tree)
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines(["*journeys/test_one.py::test_two*2 feature markers*"])


def test_missing_tier_marker_fails(pytester):
    tree = dict(FULL_TREE)
    tree["journeys/test_one.py"] = """
        import pytest

        @pytest.mark.feature("F1")
        def test_no_tier():
            pass
    """
    _suite(pytester, tree)
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines(["*journeys/test_one.py::test_no_tier*no tier marker*"])


def test_release_marker_needs_a_stage(pytester):
    tree = dict(FULL_TREE)
    tree["journeys/test_one.py"] = """
        import pytest

        @pytest.mark.feature("F13")
        @pytest.mark.release
        def test_no_stage():
            pass
    """
    _suite(pytester, tree)
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines(["*journeys/test_one.py::test_no_stage*release marker*"])


def test_coverage_gap_fails_only_an_unfiltered_run(pytester):
    tree = {k: v for k, v in FULL_TREE.items() if k != "journeys/test_scoping.py"}
    _suite(pytester, tree)
    unfiltered = _run(pytester, "--collect-only", "-q", "--target=kind-dev")
    assert unfiltered.ret == pytest.ExitCode.INTERRUPTED
    unfiltered.stdout.fnmatch_lines(["*F8 (integration only) has no test in tier integration*"])

    narrowed = _run(pytester, "--collect-only", "-q", "--target=kind-dev", "--feature=F2")
    assert narrowed.ret == 0, narrowed.stdout.str() + narrowed.stderr.str()


# Selection.


def test_kind_dev_deselects_journey_tests(pytester):
    _suite(pytester, FULL_TREE)
    result = _run(pytester, "--target=kind-dev")
    outcomes = result.parseoutcomes()
    assert result.ret == 0, result.stdout.str()
    assert outcomes == {"passed": 3, "deselected": 3}
    assert "skipped" not in outcomes


def test_nebari_runs_journey_tests(pytester):
    _suite(pytester, FULL_TREE)
    result = _run(pytester, "--target=nebari", "--collect-only", "-q")
    assert result.ret == 0, result.stdout.str()
    assert _collected(result) == [
        "journeys/test_install.py::test_converges",
        "journeys/test_models.py::test_model_ready",
    ]


def test_feature_narrows_the_run(pytester):
    _suite(pytester, FULL_TREE)
    result = _run(pytester, "--target=kind-dev", "--feature=F8, F2", "--collect-only", "-q")
    assert result.ret == 0, result.stdout.str()
    assert _collected(result) == [
        "journeys/test_models.py::test_model_ready",
        "journeys/test_scoping.py::test_spoofing_rejected",
    ]


def test_feature_outside_tier_is_usage_error(pytester):
    _suite(pytester, FULL_TREE)
    result = _run(pytester, "--target=nebari", "--feature=F8")
    assert result.ret == pytest.ExitCode.USAGE_ERROR
    result.stderr.fnmatch_lines(["*F8*not in the journey tier*"])


def test_unknown_feature_option_is_usage_error(pytester):
    _suite(pytester, FULL_TREE)
    result = _run(pytester, "--target=kind-dev", "--feature=F99")
    assert result.ret == pytest.ExitCode.USAGE_ERROR
    result.stderr.fnmatch_lines(["*F99*not in features.toml*"])


def test_release_stage_selects_only_that_stage(pytester, tmp_path):
    _suite(pytester, FULL_TREE)
    junit = tmp_path / "junit.xml"
    result = _run(
        pytester,
        "--target=nebari",
        "--release-stage=before",
        f"--state={tmp_path / 'state.json'}",
        f"--junitxml={junit}",
    )
    assert result.ret == 0, result.stdout.str()
    assert result.parseoutcomes() == {"passed": 1, "deselected": 5}
    cases = _junit_cases(junit)
    assert list(cases) == ["journeys/test_upgrade.py::test_upgrade_before"]
    assert cases["journeys/test_upgrade.py::test_upgrade_before"]["props"] == {
        "nodeid": "journeys/test_upgrade.py::test_upgrade_before",
        "feature": "F13",
        "tiers": "release",
        "stage": "before",
    }


@pytest.mark.parametrize(
    "args",
    [
        ("--target=nebari", "--release-stage=after"),
        ("--target=kind-dev", "--release-stage=after", "--state=state.json"),
        ("--release-stage=after", "--state=state.json"),
    ],
    ids=["no-state", "kind-dev", "no-target"],
)
def test_release_stage_requires_state_and_nebari(pytester, args):
    _suite(pytester, FULL_TREE)
    result = _run(pytester, *args)
    assert result.ret == pytest.ExitCode.USAGE_ERROR
    result.stderr.fnmatch_lines(["*--release-stage needs --target=nebari and --state*"])


def test_f11_collected_last(pytester):
    tree = dict(FULL_TREE)
    # Sorts after test_zz_gateway_failure.py, so only the hook puts F11 last.
    tree["journeys/test_zzz_later.py"] = """
        import pytest

        @pytest.mark.feature("F8")
        @pytest.mark.integration
        def test_later():
            pass
    """
    _suite(pytester, tree)
    result = _run(pytester, "--target=kind-dev", "--collect-only", "-q")
    assert result.ret == 0, result.stdout.str()
    collected = _collected(result)
    assert collected[-1] == "journeys/test_zz_gateway_failure.py::test_gateway_failure_heals"
    assert "journeys/test_zzz_later.py::test_later" in collected[:-1]


# Feature properties (Q69).


def test_session_fixture_error_keeps_properties(pytester, tmp_path):
    tree = dict(FULL_TREE)
    tree["journeys/test_models.py"] = """
        import pytest

        @pytest.fixture(scope="session")
        def target():
            raise RuntimeError("profile broken")

        @pytest.mark.feature("F2")
        @pytest.mark.integration
        @pytest.mark.journey
        def test_model_ready(target):
            pass

        @pytest.mark.feature("F8")
        @pytest.mark.integration
        def test_scoped(target):
            pass
    """
    _suite(pytester, tree)
    junit = tmp_path / "junit.xml"
    result = _run(pytester, "--target=kind-dev", "--feature=F2,F8", f"--junitxml={junit}")
    assert result.ret == pytest.ExitCode.TESTS_FAILED
    cases = _junit_cases(junit)
    assert sorted(cases) == [
        "journeys/test_models.py::test_model_ready",
        "journeys/test_models.py::test_scoped",
        F8_TEST,
    ]
    ready = cases["journeys/test_models.py::test_model_ready"]
    assert ready["props"] == {
        "nodeid": "journeys/test_models.py::test_model_ready",
        "feature": "F2",
        "tiers": "integration,journey",
    }
    assert cases["journeys/test_models.py::test_scoped"]["props"]["feature"] == "F8"
    for node_id in ("journeys/test_models.py::test_model_ready", "journeys/test_models.py::test_scoped"):
        assert any("profile broken" in e for e in cases[node_id]["errors"]), cases[node_id]


# Known failures (Q62, Q72).


def test_known_failure_without_issue_fails(pytester):
    _suite(pytester, FULL_TREE, _with_known_failure(f'{{ test = "{JOURNEY_ONLY_TEST}", since = "2026-11-25" }}'))
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines([f"*known failure {JOURNEY_ONLY_TEST}: has no issue*"])


def test_known_failure_for_unknown_test_fails(pytester):
    _suite(
        pytester,
        FULL_TREE,
        _with_known_failure('{ test = "journeys/test_gone.py::test_x", issue = "#1", since = "2026-11-25" }'),
    )
    result = _run(pytester, "--collect-only", "-q")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines(["*known failure journeys/test_gone.py::test_x: matches no collected test*"])


def test_known_failure_without_targets_on_journey_test_is_accepted(pytester):
    _suite(
        pytester,
        FULL_TREE,
        _with_known_failure(f'{{ test = "{JOURNEY_ONLY_TEST}", issue = "#1", since = "2026-11-25" }}'),
    )
    result = _run(pytester, "--collect-only", "-q", "--target=nebari")
    assert result.ret == 0, result.stdout.str() + result.stderr.str()


def test_known_failure_naming_kind_dev_for_journey_test_fails(pytester):
    _suite(
        pytester,
        FULL_TREE,
        _with_known_failure(
            f'{{ test = "{JOURNEY_ONLY_TEST}", issue = "#1", since = "2026-11-25", targets = ["kind-dev"] }}'
        ),
    )
    result = _run(pytester, "--collect-only", "-q", "--target=nebari")
    assert result.ret == pytest.ExitCode.INTERRUPTED
    result.stdout.fnmatch_lines(
        [f"*known failure {JOURNEY_ONLY_TEST}: lists target kind-dev, but the test runs on nebari*"]
    )


def test_known_failure_outside_feature_selection_is_accepted(pytester):
    _suite(
        pytester,
        FULL_TREE,
        _with_known_failure(f'{{ test = "{F8_TEST}", issue = "#1", since = "2026-11-25", targets = ["kind-dev"] }}'),
    )
    result = _run(pytester, "--collect-only", "-q", "--target=kind-dev", "--feature=F2")
    assert result.ret == 0, result.stdout.str() + result.stderr.str()
    assert _collected(result) == ["journeys/test_models.py::test_model_ready"]


def test_known_failure_outside_release_stage_is_accepted(pytester, tmp_path):
    _suite(
        pytester,
        FULL_TREE,
        _with_known_failure(
            f'{{ test = "{JOURNEY_ONLY_TEST}", issue = "#1", since = "2026-11-25", targets = ["nebari"] }}'
        ),
    )
    result = _run(
        pytester,
        "--collect-only",
        "-q",
        "--target=nebari",
        "--release-stage=before",
        f"--state={tmp_path / 'state.json'}",
    )
    assert result.ret == 0, result.stdout.str() + result.stderr.str()
    assert _collected(result) == ["journeys/test_upgrade.py::test_upgrade_before"]


def test_release_stage_run_skips_coverage_check(pytester, tmp_path):
    tree = {k: v for k, v in FULL_TREE.items() if k != "journeys/test_scoping.py"}  # F8 has a gap
    _suite(pytester, tree)
    result = _run(
        pytester,
        "--collect-only",
        "-q",
        "--target=nebari",
        "--release-stage=before",
        f"--state={tmp_path / 'state.json'}",
    )
    assert result.ret == 0, result.stdout.str() + result.stderr.str()
    assert _collected(result) == ["journeys/test_upgrade.py::test_upgrade_before"]
