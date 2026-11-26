"""Tests for llm_journeys.report. These need no cluster."""

from pathlib import Path

import pytest

from llm_journeys.features import KnownFailure, load_features
from llm_journeys.report import (
    exit_code,
    feature_results,
    main,
    read_junit,
    read_pins,
    render_markdown,
)

FIXTURES = Path(__file__).resolve().parent / "fixtures"
JUNIT = FIXTURES / "junit"
REPORT = FIXTURES / "report"
FEATURES_TOML = REPORT / "features.toml"
PINS = REPORT / "versions.env"
RUN_INFO = REPORT / "run-info.json"

HTTP2 = "journeys/test_usage.py::test_large_body_http2"
HTTP1 = "journeys/test_usage.py::test_large_body_http1"
JOURNEY = frozenset({"journey"})
RELEASE = frozenset({"release"})


def _features_with(tmp_path: Path, known_toml: str) -> Path:
    """The fixture features.toml with its empty known_failures replaced."""
    text = FEATURES_TOML.read_text().replace("known_failures = []\n", known_toml)
    path = tmp_path / "features.toml"
    path.write_text(text)
    return path


def _known(test: str, issue: str, targets: tuple[str, ...] = ("nebari",)) -> str:
    targets_toml = ", ".join(f'"{t}"' for t in targets)
    return f'[[known_failures]]\ntest = "{test}"\nissue = "{issue}"\nsince = "2026-11-26"\ntargets = [{targets_toml}]\n\n'


def _run(capsys, *junit: str, tier: str = "journey", features: Path = FEATURES_TOML, extra=()) -> tuple[int, str]:
    argv = []
    for name in junit:
        argv += ["--junit", str(JUNIT / name)]
    argv += ["--features", str(features), "--pins", str(PINS), "--run-info", str(RUN_INFO), "--tier", tier, *extra]
    code = main(argv)
    return code, capsys.readouterr().out


def _row(markdown: str, feature_id: str) -> str:
    rows = [line for line in markdown.splitlines() if line.startswith(f"| {feature_id} |")]
    assert len(rows) == 1, markdown
    return rows[0]


def _outcomes(*junit: str, known=(), tiers=JOURNEY, target="nebari", only=None) -> dict[str, str]:
    results = feature_results(
        load_features(FEATURES_TOML), known, read_junit([JUNIT / n for n in junit]), tiers, target, only
    )
    return {r.feature.id: r.outcome for r in results}


def test_all_passed_exit_0(capsys):
    code, out = _run(capsys, "all_passed.xml")

    assert code == 0
    for feature_id in ("F1", "F2", "F10"):
        assert "| passed |" in _row(out, feature_id)
    assert "| F13 |" not in out  # release tier only


def test_missing_feature_is_not_run_exit_1(capsys):
    code, out = _run(capsys, "missing_f10.xml")

    assert code == 1
    assert "| not run |" in _row(out, "F10")
    assert "| passed |" in _row(out, "F1")


def test_skipped_and_error_map_to_failed():
    results = read_junit([JUNIT / "skipped_and_error.xml"])

    assert {r.node_id: r.outcome for r in results} == {
        "journeys/test_install.py::test_apps_healthy": "skipped",
        "journeys/test_models.py::test_llmmodel_ready": "error",
        "journeys/test_usage.py::test_usage_recorded": "passed",
    }
    assert _outcomes("skipped_and_error.xml") == {"F1": "failed", "F2": "failed", "F10": "passed"}


def test_merge_across_files(capsys):
    assert _outcomes("f10_passed.xml", "f10_failed.xml")["F10"] == "failed"

    code, out = _run(capsys, "f10_passed.xml", "f10_failed.xml")
    assert code == 1
    assert "| failed |" in _row(out, "F10")
    assert "journeys/test_usage.py::test_usage_recorded" in _row(out, "F10")


def test_release_needs_both_stages(capsys):
    code, out = _run(capsys, "release_before.xml", tier="release")
    assert code == 1
    assert "| not run |" in _row(out, "F13")
    assert "after" in _row(out, "F13")

    code, out = _run(capsys, "release_before.xml", "release_after.xml", tier="release")
    assert code == 0
    assert "| passed |" in _row(out, "F13")


def test_known_failure_all_listed(tmp_path, capsys):
    features = _features_with(tmp_path, _known(HTTP2, "#1") + _known(HTTP1, "#2"))

    code, out = _run(capsys, "two_failing.xml", features=features)

    assert code == 0
    assert "| known failure (#1, #2) |" in _row(out, "F10")


def test_one_unlisted_failure_fails_feature(tmp_path, capsys):
    features = _features_with(tmp_path, _known(HTTP2, "#1"))

    code, out = _run(capsys, "two_failing.xml", features=features)

    assert code == 1
    row = _row(out, "F10")
    assert "| failed |" in row
    assert HTTP1 in row


def test_entry_for_other_target_has_no_effect():
    known = [
        KnownFailure(test=HTTP2, issue="#1", since="2026-11-26", targets=("nebari",)),
        KnownFailure(test=HTTP1, issue="#2", since="2026-11-26", targets=("nebari",)),
    ]

    assert _outcomes("two_failing.xml", known=known, target="nebari")["F10"] == "known failure"
    assert _outcomes("two_failing.xml", known=known, target="kind-dev")["F10"] == "failed"


def test_entry_without_targets_applies_to_the_run_target():
    known = [
        KnownFailure(test=HTTP2, issue="#1", since="2026-11-26", targets=()),
        KnownFailure(test=HTTP1, issue="#2", since="2026-11-26", targets=()),
    ]

    assert _outcomes("two_failing.xml", known=known, target="kind-dev")["F10"] == "known failure"


def test_listed_test_passing_strict_exit_1(tmp_path, capsys):
    features = _features_with(tmp_path, _known(HTTP2, "#1") + _known(HTTP1, "#2"))

    code, out = _run(capsys, "listed_passing.xml", features=features, extra=["--strict-known-failures"])

    assert code == 1
    row = _row(out, "F10")
    assert "| known failure (#2) |" in row
    assert HTTP2 in row and "remove" in row
    assert not any(line.startswith("Warning:") for line in out.splitlines())


def test_listed_test_passing_lenient_warns_exit_0(tmp_path, capsys):
    features = _features_with(tmp_path, _known(HTTP2, "#1") + _known(HTTP1, "#2"))

    code, out = _run(capsys, "listed_passing.xml", features=features)

    assert code == 0
    assert "| known failure (#2) |" in _row(out, "F10")
    warnings = [line for line in out.splitlines() if line.startswith("Warning:")]
    assert len(warnings) == 1 and HTTP2 in warnings[0]


def test_feature_flag_limits_rows(capsys):
    code, out = _run(capsys, "f10_passed.xml", extra=["--feature", "F10"])

    assert code == 0
    assert "| passed |" in _row(out, "F10")
    assert "| F1 |" not in out
    assert "| F2 |" not in out


@pytest.mark.parametrize(
    ("argv_change", "message"),
    [
        (lambda a: a[: a.index("--tier")] + a[a.index("--tier") + 2 :], "--tier"),
        (lambda a: a + ["--feature", "F99"], "F99"),
        (lambda a: a + ["--feature", "F13"], "F13"),
        (lambda a: [x.replace("run-info.json", "run-info-no-target.json") for x in a], "target"),
        (lambda a: [x.replace("versions.env", "no-such.env") for x in a], "no-such.env"),
    ],
    ids=["missing-tier", "unknown-feature", "feature-outside-tier", "run-info-without-target", "missing-pins"],
)
def test_usage_error_exit_2(capsys, argv_change, message):
    argv = [
        "--junit", str(JUNIT / "all_passed.xml"),
        "--features", str(FEATURES_TOML),
        "--pins", str(PINS),
        "--run-info", str(RUN_INFO),
        "--tier", "journey",
    ]  # fmt: skip

    code = main(argv_change(argv))

    assert code == 2
    assert message in capsys.readouterr().err


def test_markdown_shows_nic_tag_pins_and_deviations(capsys):
    code, out = _run(capsys, "all_passed.xml")

    assert code == 0
    assert "v0.17.2" in out  # NIC tag
    assert "0123abc" in out  # nebari_journeys commit
    assert "v1.19.1" in out  # cert-manager read from the cluster
    for line in ("GIE_VERSION", "v1.5.0", "AI_GATEWAY_VERSION", "v0.5.0"):
        assert line in out
    assert "NIC_VERSION=v0.17.2 (override of the latest NIC release)" in out
    assert "Usage metering \\| the usage console" in _row(out, "F10")  # a pipe cannot break the table


def test_markdown_shows_both_nic_tags_after_release():
    results = feature_results(
        load_features(FEATURES_TOML),
        (),
        read_junit([JUNIT / "release_before.xml", JUNIT / "release_after.xml"]),
        RELEASE,
        "nebari",
    )
    run_info = {"target": "nebari", "nic_tag": "v0.18.0", "before_nic_tag": "v0.17.2", "deviations": []}

    out = render_markdown(results, {}, run_info)

    assert "v0.18.0" in out and "v0.17.2" in out


def test_out_writes_file(tmp_path, capsys):
    out_path = tmp_path / "report.md"

    code, stdout = _run(capsys, "all_passed.xml", extra=["--out", str(out_path)])

    assert code == 0
    assert stdout == ""
    assert "| F10 |" in out_path.read_text()


def test_exit_code_rules():
    results = feature_results(
        load_features(FEATURES_TOML), (), read_junit([JUNIT / "all_passed.xml"]), JOURNEY, "nebari"
    )
    assert exit_code(results, strict_known_failures=True) == 0

    results = feature_results(
        load_features(FEATURES_TOML), (), read_junit([JUNIT / "missing_f10.xml"]), JOURNEY, "nebari"
    )
    assert exit_code(results, strict_known_failures=False) == 1


def test_read_pins_follows_versions_env_syntax():
    assert read_pins(PINS) == {
        "GIE_VERSION": "v1.5.0",
        "ENVOY_GATEWAY_VERSION": "v1.6.0",
        "AI_GATEWAY_VERSION": "v0.5.0",
    }


@pytest.mark.parametrize(
    "line",
    [
        "GIE_VERSION = v1.5.0",
        "GIE_VERSION =v1.5.0",
        'GIE_VERSION="v1.5.0"',
        "GIE_VERSION='v1.5.0'",
        "GIE_VERSION=v1.5.0 # pinned",
        "GIE_VERSION=$OTHER",
        "GIE_VERSION=",
        "export GIE_VERSION=v1.5.0",
        "gie-version=v1.5.0",
        "just text",
        "AI_GATEWAY_VERSION=v0.6.0",  # a duplicate key
    ],
)
def test_read_pins_rejects_malformed_line(tmp_path, line):
    path = tmp_path / "versions.env"
    path.write_text(f"# comment\nAI_GATEWAY_VERSION=v0.5.0\n{line}\n")

    with pytest.raises(ValueError, match=r"versions\.env:3:"):
        read_pins(path)


def test_report_imports_nothing_that_needs_a_cluster():
    import subprocess
    import sys

    code = (
        "import sys, llm_journeys.report; "
        "bad = sorted(m for m in sys.modules if m.split('.')[0] in "
        "{'kubernetes', 'playwright', 'requests', 'nebari_journeys', 'pytest'}); "
        "print(','.join(bad)); sys.exit(1 if bad else 0)"
    )
    done = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True)

    assert done.returncode == 0, done.stdout + done.stderr


def test_session_fixture_error_text_reaches_failed_rows(capsys):
    results = read_junit([JUNIT / "session_error.xml"])
    assert all("profile broken" in r.message and "\n" not in r.message for r in results)

    code, out = _run(capsys, "session_error.xml")

    assert code == 1
    for feature_id in ("F1", "F2", "F10"):
        row = _row(out, feature_id)
        assert "| failed |" in row
        assert "profile broken" in row


def test_message_falls_back_to_element_text_and_is_bounded(tmp_path):
    path = tmp_path / "junit.xml"
    long_line = "x" * 1000
    path.write_text(
        '<testsuites><testsuite name="pytest">'
        '<testcase classname="journeys.test_usage" name="test_usage_recorded"><properties>'
        '<property name="nodeid" value="journeys/test_usage.py::test_usage_recorded"/>'
        '<property name="feature" value="F10"/><property name="tiers" value="journey"/>'
        f"</properties><failure>first line\nsecond line {long_line}</failure></testcase>"
        '<testcase classname="journeys.test_install" name="test_apps_healthy"><properties>'
        '<property name="nodeid" value="journeys/test_install.py::test_apps_healthy"/>'
        '<property name="feature" value="F1"/><property name="tiers" value="journey"/>'
        f'</properties><failure message="{long_line}"/></testcase>'
        "</testsuite></testsuites>"
    )

    messages = {r.feature: r.message for r in read_junit([path])}

    assert messages["F10"] == "first line"
    assert 0 < len(messages["F1"]) <= 300


def test_feature_messages_align_with_failing_tests():
    known = [KnownFailure(test=HTTP2, issue="#1", since="2026-11-26", targets=())]
    results = feature_results(
        load_features(FEATURES_TOML), known, read_junit([JUNIT / "two_failing.xml"]), JOURNEY, "nebari"
    )
    f10 = next(r for r in results if r.feature.id == "F10")

    assert f10.failing_tests == (HTTP1,)
    assert f10.messages == ("AssertionError: assert 500 == 200",)


def test_release_one_stage_known_failure_is_not_run(tmp_path, capsys):
    before = "journeys/test_upgrade.py::test_upgrade_before"
    features = _features_with(tmp_path, _known(before, "#3"))

    code, out = _run(capsys, "release_before_failed.xml", tier="release", features=features)

    assert code == 1
    assert "| not run |" in _row(out, "F13")
