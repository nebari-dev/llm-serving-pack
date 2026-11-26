"""Tests for llm_journeys.features. These need no cluster."""

from pathlib import Path

import pytest

from llm_journeys.features import (
    KnownFailure,
    coverage_gaps,
    known_failure_problems,
    load_features,
    load_known_failures,
    tiers_for,
)

FEATURES_TOML = Path(__file__).resolve().parent.parent / "features.toml"

JOURNEY_TEST = "journeys/test_keys.py::test_ui_mint_key"
BOTH_TIERS_TEST = "journeys/test_access.py::test_external_api_key"
RELEASE_BEFORE_TEST = "journeys/test_upgrade.py::test_upgrade_before"

# node ID -> targets from its tier markers, over the full collected set.
FULL_SET = {
    JOURNEY_TEST: frozenset({"nebari"}),
    BOTH_TIERS_TEST: frozenset({"kind-dev", "nebari"}),
    RELEASE_BEFORE_TEST: frozenset({"nebari"}),
}


def _write(tmp_path: Path, text: str) -> Path:
    path = tmp_path / "features.toml"
    path.write_text(text)
    return path


def test_load_features_reads_all_fourteen():
    features = load_features(FEATURES_TOML)

    assert list(features) == [f"F{n}" for n in range(1, 15)]
    assert features["F12"].tiers == ("integration", "journey")
    assert features["F13"].tiers == ("release",)
    for feature_id, feature in features.items():
        assert feature.id == feature_id
        assert feature.title


def test_known_failures_key_is_not_a_feature(tmp_path):
    path = _write(
        tmp_path,
        """
[F1]
title = "Install converges"
tiers = ["journey"]

[[known_failures]]
test = "journeys/test_install.py::test_apps_healthy"
issue = "nebari-dev/llm-serving-pack#1"
since = "2026-11-26"
""",
    )

    assert list(load_features(path)) == ["F1"]
    assert "known_failures" not in load_features(FEATURES_TOML)


def test_shipped_known_failures_is_empty():
    assert load_known_failures(FEATURES_TOML) == []


def test_load_known_failures_reads_entries(tmp_path):
    path = _write(
        tmp_path,
        """
[[known_failures]]
test = "journeys/test_usage.py::test_large_body_http2"
issue = "nebari-dev/llm-serving-pack#278"
since = "2026-11-26"
targets = ["nebari"]

[[known_failures]]
test = "journeys/test_usage.py::test_large_body_http1"
issue = "nebari-dev/llm-serving-pack#278"
since = "2026-11-26"
""",
    )

    assert load_known_failures(path) == [
        KnownFailure(
            test="journeys/test_usage.py::test_large_body_http2",
            issue="nebari-dev/llm-serving-pack#278",
            since="2026-11-26",
            targets=("nebari",),
        ),
        KnownFailure(
            test="journeys/test_usage.py::test_large_body_http1",
            issue="nebari-dev/llm-serving-pack#278",
            since="2026-11-26",
            targets=(),
        ),
    ]


def test_known_failure_without_issue_rejected(tmp_path):
    path = _write(
        tmp_path,
        f"""
[[known_failures]]
test = "{JOURNEY_TEST}"
since = "2026-11-26"
""",
    )

    problems = known_failure_problems(load_known_failures(path), FULL_SET)

    assert len(problems) == 1
    assert JOURNEY_TEST in problems[0]
    assert "issue" in problems[0]


def test_known_failure_unknown_node_rejected():
    entry = KnownFailure(
        test="journeys/test_keys.py::test_gone",
        issue="nebari-dev/llm-serving-pack#1",
        since="2026-11-26",
        targets=(),
    )

    problems = known_failure_problems([entry], FULL_SET)

    assert len(problems) == 1
    assert "journeys/test_keys.py::test_gone" in problems[0]
    assert "no collected test" in problems[0]


def test_known_failure_wrong_target_rejected():
    entry = KnownFailure(
        test=JOURNEY_TEST,
        issue="nebari-dev/llm-serving-pack#1",
        since="2026-11-26",
        targets=("kind-dev",),
    )

    problems = known_failure_problems([entry], FULL_SET)

    assert len(problems) == 1
    assert JOURNEY_TEST in problems[0]
    assert "kind-dev" in problems[0]


@pytest.mark.parametrize("test_id", [JOURNEY_TEST, BOTH_TIERS_TEST])
def test_known_failure_targets_default_to_test_tiers(tmp_path, test_id):
    path = _write(
        tmp_path,
        f"""
[[known_failures]]
test = "{test_id}"
issue = "nebari-dev/llm-serving-pack#1"
since = "2026-11-26"
""",
    )

    known = load_known_failures(path)

    assert known[0].targets == ()
    assert known_failure_problems(known, FULL_SET) == []


def test_known_failure_checked_against_full_set():
    # A --feature F4 run on nebari selects only JOURNEY_TEST; a
    # --release-stage=after run selects none of these. The check is given the
    # full collected set, so entries for tests outside either selection pass.
    known = [
        KnownFailure(
            test=BOTH_TIERS_TEST,
            issue="nebari-dev/llm-serving-pack#1",
            since="2026-11-26",
            targets=("nebari",),
        ),
        KnownFailure(
            test=RELEASE_BEFORE_TEST,
            issue="nebari-dev/llm-serving-pack#2",
            since="2026-11-26",
            targets=("nebari",),
        ),
    ]

    assert known_failure_problems(known, FULL_SET) == []


@pytest.mark.parametrize(
    "target,stage,expected",
    [
        ("kind-dev", None, {"integration"}),
        ("nebari", None, {"journey"}),
        ("nebari", "before", {"release"}),
        ("nebari", "after", {"release"}),
    ],
)
def test_tiers_for(target, stage, expected):
    assert tiers_for(target, stage) == frozenset(expected)


def test_tiers_for_rejects_stage_on_kind_dev():
    with pytest.raises(ValueError, match="nebari"):
        tiers_for("kind-dev", "before")


def test_coverage_gap_names_feature_and_tier():
    features = load_features(FEATURES_TOML)
    collected = [
        (feature_id, frozenset(feature.tiers))
        for feature_id, feature in features.items()
        if feature_id != "F12"
    ]
    collected.append(("F12", frozenset({"integration"})))

    gaps = coverage_gaps(features, collected)

    assert len(gaps) == 1
    assert "F12" in gaps[0]
    assert "journey" in gaps[0]


def test_coverage_gaps_empty_when_every_tier_has_a_test():
    features = load_features(FEATURES_TOML)
    collected = [
        (feature_id, frozenset({tier}))
        for feature_id, feature in features.items()
        for tier in feature.tiers
    ]

    assert coverage_gaps(features, collected) == []
