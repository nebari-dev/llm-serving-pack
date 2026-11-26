"""Suite fixtures and the collection hook.

The hook enforces the features.toml rules on every test under this
directory, deselects what the run does not select, and puts the feature
properties on each remaining item, so they reach the JUnit file even when a
session fixture fails before any test body runs (Q69).

The session fixtures open the Target named by --target and --kube-context,
take the session lease (R6-F9), run preflight once under it and sweep what
crashed runs left behind. Per-test objects are made by factories (scratch_user,
scratch_model, api_key) and removed at teardown unless --keep is given.
pytest_sessionfinish writes results/run-info.json, also when the target
could not be opened.
"""

import json
import logging
import os
from collections.abc import Callable
from pathlib import Path

import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from nebari_journeys.keycloak import generated_password, scratch_username

from llm_journeys import models
from llm_journeys.features import (
    coverage_gaps,
    known_failure_problems,
    load_features,
    load_known_failures,
    tiers_for,
)
from llm_journeys.keys import KeyManagerClient
from llm_journeys.preflight import preflight
from llm_journeys.release import read_state
from llm_journeys.target import SessionLease, connect_cluster, open_target, session_lease
from llm_journeys.tokens import token_source

log = logging.getLogger(__name__)

SUITE_ROOT = Path(__file__).resolve().parent.parent  # tests/journeys/; node IDs are relative to it
JOURNEYS_DIR = Path(__file__).resolve().parent
FEATURES_TOML = SUITE_ROOT / "features.toml"

# The inverse of llm_journeys.features.tiers_for; change the two together.
TIER_TARGETS = {"integration": "kind-dev", "journey": "nebari", "release": "nebari"}
RELEASE_STAGES = ("before", "after")
LAST_FEATURE = "F11"  # changes the operator's gateway setting, so nothing may run after it (Q37)

RESULTS_DIR = SUITE_ROOT / "results"
RUN_INFO = RESULTS_DIR / "run-info.json"
HELPERS_JSON = RESULTS_DIR / "helpers.json"  # written by install-helpers.sh
# Components NIC provides on nebari; on kind-dev `make setup` installs the same Deployments.
COMPONENT_DEPLOYMENTS = {
    "envoy-gateway": ("envoy-gateway-system", "envoy-gateway"),
    "cert-manager": ("cert-manager", "cert-manager"),
}
SESSION_FIXTURES = ("cluster", "target")  # an error in either is recorded in run-info.json
ERROR_TEXT_LIMIT = 500

RUN_INFO_KEY = pytest.StashKey[dict]()  # what the session learned for run-info.json
LEASE_KEY = pytest.StashKey[SessionLease]()


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


# Session fixtures.


@pytest.fixture(scope="session")
def cluster(request):
    """The cluster on --kube-context, refused unless it is a local kind cluster (Q47)."""
    name = request.config.getoption("--target")
    kube_context = request.config.getoption("--kube-context")
    if name is None or kube_context is None:
        raise pytest.UsageError("--target and --kube-context are required to run the suite's tests")
    with connect_cluster(kube_context) as connected:
        yield connected


def _component_versions(apps) -> dict[str, str | None]:
    """name -> image tag of the component's Deployment; None, with the reason logged, when it cannot be read.

    Best effort for run-info.json: no failure here may fail the target fixture.
    """
    versions: dict[str, str | None] = {}
    for name, (namespace, deployment) in COMPONENT_DEPLOYMENTS.items():
        try:
            spec = apps.read_namespaced_deployment(name=deployment, namespace=namespace).spec
            containers = spec.template.spec.containers
            if not containers:
                raise ValueError("it has no containers")
            image = containers[0].image.split("@", 1)[0]
        except Exception as error:  # noqa: BLE001 - API, transport or shape errors all mean "unknown"
            if isinstance(error, ApiException):
                reason = f"{error.status} {error.reason}"
            else:
                reason = f"{type(error).__name__}: {error}"
            log.warning("cannot read the %s version from Deployment %s/%s for run-info.json: %s",
                        name, namespace, deployment, reason)
            versions[name] = None
            continue
        repository, _, tag = image.rpartition(":")
        versions[name] = tag if repository and "/" not in tag else None
    return versions


@pytest.fixture(scope="session")
def target(request, cluster, tmp_path_factory):
    """The Target, held under the session lease for the whole run, preflight-checked once under it."""
    config = request.config
    with open_target(
        config.getoption("--target"),
        config.getoption("--kube-context"),
        cluster,
        tmp_path_factory.mktemp("target"),
        gateway_https_port=config.getoption("--gateway-https-port"),
    ) as opened:
        config.stash.setdefault(RUN_INFO_KEY, {}).update(
            nic_tag=opened.nic_tag,
            deviations=list(opened.deviations),
            components=_component_versions(client.AppsV1Api(opened.cluster.core.api_client)),
        )
        # The lease first, so a second concurrent run fails with LeaseHeld, not
        # with whatever the first run's work makes preflight see. Released on
        # any later failure by session_lease.
        with session_lease(opened) as lease:
            problems = preflight(opened, release_stage=config.getoption("--release-stage"))
            if problems:
                raise RuntimeError(
                    f"the {opened.name} profile on {opened.kube_context} is incomplete:\n  " + "\n  ".join(problems)
                )
            config.stash[LEASE_KEY] = lease
            yield opened


@pytest.fixture(scope="session")
def release_state_path(request) -> Path:
    """--state, else the default the sweep reads to spare the F13 objects (R6-F9)."""
    state = request.config.getoption("--state")
    return Path(state) if state is not None else models.DEFAULT_STATE_PATH


@pytest.fixture(scope="session")
def sweep_leftovers(request, target, release_state_path) -> None:
    """Remove what crashed runs left behind: journey models and scratch users; off with --release-stage.

    Runs under the session lease, so it never sweeps another run's objects.
    """
    if request.config.getoption("--release-stage") is not None:
        return
    results = models.sweep_leftovers(target, release_state_path)
    left = []
    for what, result in results.items():
        if result.deleted:
            log.info("session sweep removed %s: %s", what, ", ".join(result.deleted))
        for item in result.skipped:
            log.warning("session sweep left %s %s alone: it matched the sweep's search but not both guards "
                        "(users: journey- prefix and the scratch email domain; models: test label and journey- prefix)",
                        what, item)
        left += [f"{what}: {item}" for item in result.failed]
    if left:
        raise RuntimeError("the session sweep could not remove:\n  " + "\n  ".join(left))


@pytest.fixture(autouse=True)
def _session_guard(request) -> None:
    """For every test that uses the target: sweep first (once), and fail now if the lease was lost.

    Not unconditional: a test that needs no cluster (the collection tests'
    trees) must not need --kube-context.
    """
    if "target" not in request.fixturenames:
        return
    request.getfixturevalue("sweep_leftovers")
    lease = request.config.stash.get(LEASE_KEY, None)
    if lease is not None:
        lease.check()


# Per-test factories. Each removes what it made at teardown, unless --keep.


def _teardown(request, what: str, made: list, label: Callable, delete: Callable) -> None:
    if not made:
        return
    if request.config.getoption("--keep"):
        log.warning("--keep: leaving %s %s", what, ", ".join(label(item) for item in made))
        return
    failures = []
    for item in reversed(made):
        try:
            delete(item)
        except Exception as error:  # noqa: BLE001 - try every delete, then report them all
            failures.append(f"{label(item)}: {type(error).__name__}: {error}")
    if failures:
        raise RuntimeError(f"could not remove {what}:\n  " + "\n  ".join(failures))


@pytest.fixture
def user_token(target, request) -> Callable[[str, str], str]:
    """A currently valid token for (username, password) at each call; a browser only when it is used."""
    return token_source(target, lambda: request.getfixturevalue("new_context")()).token


@pytest.fixture
def scratch_user(target, request):
    """factory(groups=()) -> (username, password) of a new journey- realm user."""
    made: list[tuple[str, str]] = []  # (user id, username)

    def make(groups=()) -> tuple[str, str]:
        username, password = scratch_username(), generated_password()
        user_id = target.keycloak.create_user(username, password)
        made.append((user_id, username))
        for group in groups:
            target.keycloak.add_user_to_group(user_id, group)
        return username, password

    yield make
    _teardown(request, "users", made, lambda item: item[1], lambda item: target.keycloak.delete_user(item[0]))


@pytest.fixture
def scratch_model(target, request):
    """factory(public=True, groups=(), served_name=None) -> the Ready LLMModel object, served by the mock image."""
    made: list[str] = []

    def make(*, public: bool = True, groups=(), served_name: str | None = None, timeout: float = 180) -> dict:
        name = models.new_model_name()
        manifest = models.mock_llm_model(target, name, served_name or f"test/{name}", public=public, groups=groups)
        models.apply_model(target, manifest)
        made.append(name)
        return models.wait_ready(target, "LLMModel", name, timeout=timeout)

    yield make
    # delete_model also removes what issue #194 leaves of a journey model.
    _teardown(request, "models", made, lambda name: f"LLMModel/{name}",
              lambda name: models.delete_model(target, "LLMModel", name))


@pytest.fixture
def api_key(target, scratch_user, user_token, request):
    """factory(model, user=None) -> ApiKey minted through the key REST API; revoked at teardown.

    `model` is the model's metadata.name. Without `user` (username, password)
    a scratch user in target.llm_group mints it.
    """
    made: list[tuple[KeyManagerClient, object]] = []

    def make(model: str, user: tuple[str, str] | None = None, description: str = "journey test key"):
        username, password = user or scratch_user(groups=[target.llm_group])
        keys = KeyManagerClient(target.key_manager, lambda: user_token(username, password))
        key = keys.create_key(model, description)
        made.append((keys, key))
        return key

    def revoke(item) -> None:
        keys, key = item
        if any(listed["clientId"] == key.client_id for listed in keys.list_keys()):  # a test may have revoked it
            keys.revoke_key(key)

    yield make
    _teardown(request, "API keys", made, lambda item: f"{item[1].model}/{item[1].client_id}", revoke)


# run-info.json (spec "JUnit XML and run-info.json").


class _SessionErrorRecorder:
    """Records the first error of the cluster or target fixture for run-info.json.

    A plugin, not a conftest hook: pytest calls pytest_fixture_setup for a
    session fixture through the session's hooks, which leave out conftests
    below the rootdir, such as this one.
    """

    @pytest.hookimpl(wrapper=True)
    def pytest_fixture_setup(self, fixturedef, request):
        try:
            return (yield)
        except BaseException as error:
            if fixturedef.argname in SESSION_FIXTURES:
                text = f"{fixturedef.argname} fixture: {type(error).__name__}: {error}"
                request.config.stash.setdefault(RUN_INFO_KEY, {}).setdefault("error", text[:ERROR_TEXT_LIMIT])
            raise


def pytest_configure(config: pytest.Config) -> None:
    config.pluginmanager.register(_SessionErrorRecorder(), "llm-journeys-session-errors")


def _helper() -> dict | None:
    try:
        data = json.loads(HELPERS_JSON.read_text())
        return {"tag": data["tag"], "commit": data["commit"]}
    except (OSError, ValueError, KeyError, TypeError):
        return None


def _before_nic_tag(config: pytest.Config) -> str | None:
    if config.getoption("--release-stage") != "after" or config.getoption("--state") is None:
        return None
    try:
        return read_state(Path(config.getoption("--state"))).nic_tag
    except (OSError, ValueError) as error:
        log.warning("run-info.json: no before_nic_tag: %s", error)
        return None


def pytest_sessionfinish(session: pytest.Session, exitstatus) -> None:
    config = session.config
    name = config.getoption("--target")
    if name is None:  # the report needs a target; a run without one is a usage error already
        return
    learned = config.stash.get(RUN_INFO_KEY, {})
    deviations = list(learned.get("deviations", ()))
    nic_version = os.environ.get("NIC_VERSION")
    if nic_version and not any(d.startswith("NIC_VERSION=") for d in deviations):
        deviations.append(f"NIC_VERSION={nic_version} (override of the latest NIC release)")
    run_info = {
        "target": name,
        "kube_context": config.getoption("--kube-context"),
        "nic_tag": learned.get("nic_tag"),
        "before_nic_tag": _before_nic_tag(config),
        "helper": _helper(),
        "components": learned.get("components"),
        "deviations": deviations,
        "error": learned.get("error"),
    }
    RESULTS_DIR.mkdir(exist_ok=True)
    RUN_INFO.write_text(json.dumps(run_info, indent=2) + "\n")
