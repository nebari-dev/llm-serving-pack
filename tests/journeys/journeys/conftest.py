"""Suite fixtures and the collection hook.

The hook enforces the features.toml rules on every test under this
directory, deselects what the run does not select, and puts the feature
properties on each remaining item, so they reach the JUnit file even when a
session fixture fails before any test body runs (Q69).
"""

from pathlib import Path

import pytest

from llm_journeys.features import (
    coverage_gaps,
    known_failure_problems,
    load_features,
    load_known_failures,
    tiers_for,
)

SUITE_ROOT = Path(__file__).resolve().parent.parent  # tests/journeys/; node IDs are relative to it
JOURNEYS_DIR = Path(__file__).resolve().parent
FEATURES_TOML = SUITE_ROOT / "features.toml"

# The inverse of llm_journeys.features.tiers_for; change the two together.
TIER_TARGETS = {"integration": "kind-dev", "journey": "nebari", "release": "nebari"}
RELEASE_STAGES = ("before", "after")
LAST_FEATURE = "F11"  # changes the operator's gateway setting, so nothing may run after it (Q37)


def _node_id(item: pytest.Item) -> str:
    """The node ID relative to tests/journeys/, whatever the rootdir is."""
    path = Path(item.path).resolve().relative_to(SUITE_ROOT).as_posix()
    _, sep, rest = item.nodeid.partition("::")
    return path + sep + rest


def _read_markers(item: pytest.Item, node_id: str, features) -> tuple[dict | None, list[str]]:
    problems: list[str] = []
    feature_marks = list(item.iter_markers("feature"))
    feature = None
    if not feature_marks:
        problems.append(f"{node_id}: no feature marker")
    elif len(feature_marks) > 1:
        problems.append(f"{node_id}: {len(feature_marks)} feature markers, expected exactly one")
    else:
        args = feature_marks[0].args
        if len(args) != 1 or not isinstance(args[0], str):
            problems.append(f'{node_id}: the feature marker takes one ID, such as feature("F5")')
        elif args[0] not in features:
            problems.append(f"{node_id}: feature {args[0]} is not in features.toml")
        else:
            feature = args[0]

    tiers = {tier for tier in TIER_TARGETS if item.get_closest_marker(tier) is not None}
    if not tiers:
        problems.append(f"{node_id}: no tier marker (integration, journey or release)")

    stage = None
    release_marks = list(item.iter_markers("release"))
    if release_marks:
        stages = {m.args[0] if len(m.args) == 1 else None for m in release_marks}
        if len(stages) != 1 or next(iter(stages)) not in RELEASE_STAGES:
            problems.append(f'{node_id}: the release marker takes one stage, release("before") or release("after")')
        else:
            stage = next(iter(stages))

    if problems:
        return None, problems
    return {"node_id": node_id, "feature": feature, "tiers": frozenset(tiers), "stage": stage}, []


def _feature_filter(config: pytest.Config, features, selected_tiers) -> frozenset[str] | None:
    raw = config.getoption("--feature")
    if raw is None:
        return None
    wanted = frozenset(part.strip() for part in raw.split(",") if part.strip())
    if not wanted:
        raise pytest.UsageError("--feature names no feature ID")
    for feature_id in sorted(wanted):
        if feature_id not in features:
            raise pytest.UsageError(f"--feature {feature_id} is not in features.toml")
        if selected_tiers is not None and not selected_tiers & set(features[feature_id].tiers):
            raise pytest.UsageError(
                f"--feature {feature_id} is not in the {' or '.join(sorted(selected_tiers))} tier "
                f"this run selects; its tiers are {', '.join(features[feature_id].tiers)}"
            )
    return wanted


def pytest_collection_modifyitems(config: pytest.Config, items: list[pytest.Item]) -> None:
    target = config.getoption("--target")
    stage = config.getoption("--release-stage")
    if stage is not None and (target != "nebari" or config.getoption("--state") is None):
        raise pytest.UsageError("--release-stage needs --target=nebari and --state")

    features = load_features(FEATURES_TOML)
    selected_tiers = tiers_for(target, stage) if target is not None else None
    only = _feature_filter(config, features, selected_tiers)

    suite = [item for item in items if Path(item.path).resolve().is_relative_to(JOURNEYS_DIR)]

    info: dict[pytest.Item, dict] = {}
    problems: list[str] = []
    for item in suite:
        found, item_problems = _read_markers(item, _node_id(item), features)
        problems.extend(item_problems)
        if found is not None:
            info[item] = found
    if problems:
        pytest.exit("collection failed:\n  " + "\n  ".join(problems), returncode=pytest.ExitCode.INTERRUPTED)

    # On the full collected set, before any deselection (Q72).
    collected_targets = {
        found["node_id"]: frozenset(TIER_TARGETS[tier] for tier in found["tiers"]) for found in info.values()
    }
    problems = known_failure_problems(load_known_failures(FEATURES_TOML), collected_targets)
    if only is None and stage is None:  # unfiltered runs only
        problems += coverage_gaps(features, ((found["feature"], found["tiers"]) for found in info.values()))
    if problems:
        pytest.exit("collection failed:\n  " + "\n  ".join(problems), returncode=pytest.ExitCode.INTERRUPTED)

    def selected(item: pytest.Item) -> bool:
        found = info.get(item)
        if found is None:  # outside journeys/: not this hook's to select
            return True
        if selected_tiers is not None and not found["tiers"] & selected_tiers:
            return False
        if stage is not None and found["stage"] != stage:
            return False
        return only is None or found["feature"] in only

    kept = [item for item in items if selected(item)]
    deselected = [item for item in items if not selected(item)]
    if deselected:
        config.hook.pytest_deselected(items=deselected)
    kept.sort(key=lambda item: item in info and info[item]["feature"] == LAST_FEATURE)  # stable
    items[:] = kept

    for item in kept:
        found = info.get(item)
        if found is None:
            continue
        item.user_properties.append(("nodeid", found["node_id"]))
        item.user_properties.append(("feature", found["feature"]))
        item.user_properties.append(("tiers", ",".join(sorted(found["tiers"]))))
        if found["stage"] is not None:
            item.user_properties.append(("stage", found["stage"]))
