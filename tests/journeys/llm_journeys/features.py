"""features.toml: the feature list, the known failures, and the rules the
collection hook enforces with them.

features.toml has one table per feature ([F5] with title and tiers) and a
reserved top-level key known_failures, which is not a feature.
"""

import tomllib
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Literal, get_args

Tier = Literal["integration", "journey", "release"]
ReleaseStage = Literal["before", "after"]

KNOWN_FAILURES_KEY = "known_failures"
_TIERS = frozenset(get_args(Tier))


@dataclass(frozen=True)
class KnownFailure:  # one per test (Q62)
    test: str  # pytest node ID relative to tests/journeys/
    issue: str  # required; empty when the entry has none, which known_failure_problems rejects
    since: str  # ISO date
    targets: tuple[str, ...]  # "kind-dev", "nebari"; empty means every target the test runs on


@dataclass(frozen=True)
class Feature:
    id: str  # "F5"
    title: str
    tiers: tuple[Tier, ...]


def _read(path: Path) -> dict:
    with open(path, "rb") as f:
        return tomllib.load(f)


def load_features(path: Path) -> dict[str, Feature]:
    features: dict[str, Feature] = {}
    for key, table in _read(path).items():
        if key == KNOWN_FAILURES_KEY:
            continue
        tiers = tuple(table["tiers"])
        unknown = [t for t in tiers if t not in _TIERS]
        if unknown:
            raise ValueError(f"{path}: {key} has unknown tiers {unknown}; allowed: {sorted(_TIERS)}")
        features[key] = Feature(id=key, title=table["title"], tiers=tiers)
    return features


def load_known_failures(path: Path) -> list[KnownFailure]:
    return [
        KnownFailure(
            test=entry["test"],
            issue=entry.get("issue", ""),
            since=entry.get("since", ""),
            targets=tuple(entry.get("targets", ())),
        )
        for entry in _read(path).get(KNOWN_FAILURES_KEY, [])
    ]


def known_failure_problems(
    known: Sequence[KnownFailure], collected: Mapping[str, frozenset[str]]
) -> list[str]:
    """Problems with the known-failure entries, empty when there are none.

    collected maps every collected node ID to the targets its tier markers
    run it on. Call it with the full collected set, before any deselection by
    tier, --feature or --release-stage, so a narrowed run never rejects an
    entry for a test it does not select (Q72).
    """
    problems: list[str] = []
    for entry in known:
        if not entry.issue:
            problems.append(f"known failure {entry.test}: has no issue")
        test_targets = collected.get(entry.test)
        if test_targets is None:
            problems.append(f"known failure {entry.test}: matches no collected test")
            continue
        for target in entry.targets:
            if target not in test_targets:
                runs_on = ", ".join(sorted(test_targets)) or "no target"
                problems.append(
                    f"known failure {entry.test}: lists target {target}, but the test runs on {runs_on}"
                )
    return problems


def tiers_for(target: Literal["kind-dev", "nebari"], stage: ReleaseStage | None) -> frozenset[Tier]:
    if stage is not None:
        if target != "nebari":
            raise ValueError(f"--release-stage is only valid with --target=nebari, not {target}")
        return frozenset({"release"})
    if target == "kind-dev":
        return frozenset({"integration"})
    if target == "nebari":
        return frozenset({"journey"})
    raise ValueError(f"unknown target {target!r}")


def coverage_gaps(
    features: Mapping[str, Feature], collected: Iterable[tuple[str, frozenset[Tier]]]
) -> list[str]:
    """One message per feature tier that has no collected test.

    collected holds (feature ID, tiers from the tier markers) per test.
    """
    covered: set[tuple[str, str]] = set()
    for feature_id, tiers in collected:
        covered.update((feature_id, tier) for tier in tiers)
    return [
        f"{feature_id} ({feature.title}) has no test in tier {tier}"
        for feature_id, feature in features.items()
        for tier in feature.tiers
        if (feature_id, tier) not in covered
    ]
