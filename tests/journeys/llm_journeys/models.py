"""Model verbs: per-test LLMModels on the target, their readiness, and the session sweep.

Every object a test creates is named `journey-<id>` and labelled
nebari.dev/test-journey=true. Every call goes through Target.cluster, which
is pinned to the --kube-context the run named.

Issue #194 workaround: deleting an LLMModel or PassthroughModel leaves its
AIGatewayRoutes, SecurityPolicies and InferencePool behind, and an orphaned
route can break API-key auth for every later request on the cluster. So
delete_model and the session sweep also delete those objects for journey
models, and wait until they are gone. Remove the workaround (ISSUE_194_*,
_remove_issue_194_leftovers and its callers) once #194 is fixed.
"""

import logging
import time
import uuid
from collections.abc import Callable, Collection, Iterable, Sequence
from pathlib import Path

import urllib3
from kubernetes.client.rest import ApiException
from nebari_journeys.keycloak import sweep_scratch_users
from nebari_journeys.sweep import SweepResult

from llm_journeys.release import read_state
from llm_journeys.target import JOURNEY_LABEL, NOT_FOUND, Target

log = logging.getLogger(__name__)

GROUP = "llm.nebari.dev"
VERSION = "v1alpha1"
PLURALS = {"LLMModel": "llmmodels", "PassthroughModel": "passthroughmodels"}
JOURNEY_PREFIX = "journey-"
ENDPOINT_CONDITIONS = ("ExternalEndpointReady", "InternalEndpointReady")

# The label the operator puts on everything it makes for a model
# (operator/internal/controller/reconcilers/common.go:49 StandardLabels,
# passthrough.go:55 PassthroughStandardLabels).
MODEL_LABEL = "llm.nebari.dev/model"
# What a deleted model leaves behind (issue #194): kind -> (group, version, plural),
# as the operator writes them (reconcilers/routing.go:25, auth.go:154, inferencepool.go:127).
ISSUE_194_LEFTOVERS = {
    "AIGatewayRoute": ("aigateway.envoyproxy.io", "v1beta1", "aigatewayroutes"),
    "SecurityPolicy": ("gateway.envoyproxy.io", "v1alpha1", "securitypolicies"),
    "InferencePool": ("inference.networking.k8s.io", "v1", "inferencepools"),
}

# The F13 state file a plain run reads when --state is not given (R6-F9).
DEFAULT_STATE_PATH = Path(__file__).resolve().parent.parent / "results" / "f13-state.json"

# The admission webhook answers 500 "failed calling webhook" while it is not
# serving yet (failurePolicy: Fail, dev/manifests/webhook.yaml); a refusal by
# the webhook itself is a 4xx and is not retried.
WEBHOOK_NOT_SERVING = "failed calling webhook"
SERVER_ERROR = 500

APPLY_TIMEOUT = 60.0
DELETE_TIMEOUT = 120.0
POLL_INTERVAL = 2.0


def _plural(kind: str) -> str:
    try:
        return PLURALS[kind]
    except KeyError:
        raise ValueError(f"unknown model kind {kind!r}: want one of {', '.join(PLURALS)}") from None


def _describe(error: BaseException) -> str:
    if isinstance(error, ApiException):
        return f"ApiException {error.status} {error.reason}"
    return f"{type(error).__name__}: {error}"


def _poll(fetch: Callable[[], object], done: Callable[[object], bool], *, timeout: float, interval: float,
          what: str, last: Callable[[object], str]) -> object:
    """fetch() until done(value); on timeout raise TimeoutError naming `what` and last(value)."""
    deadline = time.monotonic() + timeout
    while True:
        value = fetch()
        if done(value):
            return value
        if time.monotonic() >= deadline:
            raise TimeoutError(f"timed out after {timeout:g}s waiting for {what}: {last(value)}")
        time.sleep(interval)


def new_model_name() -> str:
    """A fresh per-test model name, journey-<8 hex>."""
    return f"{JOURNEY_PREFIX}{uuid.uuid4().hex[:8]}"


def mock_llm_model(target: Target, name: str, served_name: str, *, public: bool, groups: Sequence[str] = ()) -> dict:
    """An LLMModel served by the profile's mock vLLM image, as dev/test-model-scope.sh deployed them."""
    if not name.startswith(JOURNEY_PREFIX) or name == JOURNEY_PREFIX:
        raise ValueError(f"model name {name!r}: per-test objects are named {JOURNEY_PREFIX}<id>")
    if not target.mock_vllm_image:
        raise ValueError("the target has no mock vLLM image (the provisioning record names none)")
    access: dict = {"public": public}
    if groups:
        access["groups"] = list(groups)
    return {
        "apiVersion": f"{GROUP}/{VERSION}",
        "kind": "LLMModel",
        "metadata": {"name": name, "namespace": target.namespace, "labels": {JOURNEY_LABEL: "true"}},
        "spec": {
            "model": {
                "name": served_name,
                "source": "huggingface",
                "storage": {"type": "emptyDir", "size": "1Gi"},
                "preload": False,
            },
            "resources": {
                "gpu": {"count": 0, "type": "nvidia"},
                "requests": {"cpu": "100m", "memory": "128Mi"},
                "limits": {"cpu": "200m", "memory": "256Mi"},
            },
            # The operator defaults the command to the vLLM entrypoint; the
            # mock image runs a plain Python server (dev/manifests/test-model.yaml).
            "serving": {
                "image": target.mock_vllm_image,
                "command": ["python", "/server.py"],
                "replicas": 1,
                "monitoring": {"enabled": False},
            },
            "access": access,
            "endpoints": {"external": {"enabled": True, "subdomain": name}, "internal": {"enabled": True}},
        },
    }


def apply_model(
    target: Target, manifest: dict, *, timeout: float = APPLY_TIMEOUT, interval: float = POLL_INTERVAL
) -> None:
    """Create the model; retries while the admission webhook is not serving, raises any other refusal."""
    kind = manifest["kind"]
    name = manifest["metadata"]["name"]
    deadline = time.monotonic() + timeout
    while True:
        try:
            target.cluster.custom.create_namespaced_custom_object(
                group=GROUP, version=VERSION, namespace=target.namespace, plural=_plural(kind), body=manifest
            )
            return
        except ApiException as error:
            if error.status != SERVER_ERROR or WEBHOOK_NOT_SERVING not in (error.body or ""):
                raise
            if time.monotonic() >= deadline:
                raise TimeoutError(
                    f"timed out after {timeout:g}s creating {kind} {target.namespace}/{name}: "
                    "the operator's admission webhook is still not serving"
                ) from error
        time.sleep(interval)


def _get(target: Target, kind: str, name: str) -> dict | None:
    try:
        return target.cluster.custom.get_namespaced_custom_object(
            group=GROUP, version=VERSION, namespace=target.namespace, plural=_plural(kind), name=name
        )
    except ApiException as error:
        if error.status == NOT_FOUND:
            return None
        raise


def _get_existing(target: Target, kind: str, name: str) -> dict:
    obj = _get(target, kind, name)
    if obj is None:
        raise RuntimeError(f"{kind} {target.namespace}/{name} does not exist")
    return obj


def _conditions(obj: dict) -> dict[str, tuple[str, str]]:
    conditions = (obj.get("status") or {}).get("conditions") or []
    return {c["type"]: (c.get("status", ""), c.get("reason", "")) for c in conditions if "type" in c}


def endpoint_conditions(obj: dict) -> dict[str, tuple[str, str]]:
    """ExternalEndpointReady and InternalEndpointReady, type -> (status, reason), where present."""
    return {t: v for t, v in _conditions(obj).items() if t in ENDPOINT_CONDITIONS}


def _phase(obj: dict) -> str:
    return (obj.get("status") or {}).get("phase") or "no phase yet"


def wait_ready(target: Target, kind: str, name: str, *, timeout: float = 180, interval: float = POLL_INTERVAL) -> dict:
    """The model once status.phase is Ready; TimeoutError names the last phase and conditions."""
    _plural(kind)
    return _poll(
        lambda: _get_existing(target, kind, name),
        lambda obj: _phase(obj) == "Ready",
        timeout=timeout,
        interval=interval,
        what=f"{kind} {target.namespace}/{name} to be Ready",
        last=lambda obj: f"phase {_phase(obj)}, conditions {_conditions(obj)}",
    )


def wait_condition(target: Target, kind: str, name: str, condition: str, *, status: str, reason: str,
                   timeout: float, interval: float = POLL_INTERVAL) -> dict:
    """The model once its `condition` has this status and reason."""
    _plural(kind)
    return _poll(
        lambda: _get_existing(target, kind, name),
        lambda obj: _conditions(obj).get(condition) == (status, reason),
        timeout=timeout,
        interval=interval,
        what=f"{kind} {target.namespace}/{name} condition {condition} {status}/{reason}",
        last=lambda obj: f"it is {'/'.join(_conditions(obj).get(condition, ('absent',)))}",
    )


def inject_api_key(target: Target, model: str, client_id: str, secret: Callable[[], str]) -> None:
    """Add a key to <model>-api-keys as the key-manager does (data key = client ID, value = secret()).

    The key comes from a closure, never a parameter, and an API or transport
    error is re-raised without its chain: a traceback prints each frame's
    arguments, and the kube client's frames carry the patch body. A failure
    names the Secret and the cause only; the key never reaches a message.
    """
    name = f"{model}-api-keys"
    try:
        target.cluster.core.patch_namespaced_secret(
            name=name, namespace=target.namespace, body={"stringData": {client_id: secret()}}
        )
    except ApiException as error:
        raise RuntimeError(
            f"adding client {client_id} to Secret {target.namespace}/{name} failed: {error.status} {error.reason}"
        ) from None
    except urllib3.exceptions.HTTPError as error:
        raise RuntimeError(
            f"adding client {client_id} to Secret {target.namespace}/{name} failed: {type(error).__name__}"
        ) from None


def _wait_gone(fetch: Callable[[], object], *, timeout: float, interval: float, what: str) -> None:
    _poll(fetch, lambda obj: obj is None, timeout=timeout, interval=interval, what=f"{what} to be deleted",
          last=lambda obj: "it still exists")


def _delete_custom(target: Target, gvp: tuple[str, str, str], name: str, *, timeout: float, interval: float,
                   what: str) -> None:
    group, version, plural = gvp
    call = {"group": group, "version": version, "namespace": target.namespace, "plural": plural, "name": name}
    try:
        target.cluster.custom.delete_namespaced_custom_object(**call)
    except ApiException as error:
        if error.status != NOT_FOUND:
            raise

    def fetch():
        try:
            return target.cluster.custom.get_namespaced_custom_object(**call)
        except ApiException as error:
            if error.status == NOT_FOUND:
                return None
            raise

    _wait_gone(fetch, timeout=timeout, interval=interval, what=what)


def _issue_194_leftovers(target: Target, model_names: Callable[[str], bool]) -> list[tuple[str, str]]:
    """(kind, name) of every route, policy and pool in the namespace whose model label passes model_names."""
    found = []
    for kind, (group, version, plural) in ISSUE_194_LEFTOVERS.items():
        items = target.cluster.custom.list_namespaced_custom_object(
            group=group, version=version, namespace=target.namespace, plural=plural, label_selector=MODEL_LABEL
        ).get("items", [])
        for obj in items:
            model = (obj["metadata"].get("labels") or {}).get(MODEL_LABEL, "")
            if model.startswith(JOURNEY_PREFIX) and model_names(model):
                found.append((kind, obj["metadata"]["name"]))
    return found


def _remove_issue_194_leftovers(target: Target, leftovers: Iterable[tuple[str, str]], *, timeout: float,
                                interval: float) -> list[str]:
    """Delete the leftovers and wait for each; returns "Kind/name" of each deleted. Issue #194 workaround."""
    removed = []
    for kind, name in leftovers:
        _delete_custom(target, ISSUE_194_LEFTOVERS[kind], name, timeout=timeout, interval=interval,
                       what=f"{kind} {target.namespace}/{name}")
        removed.append(f"{kind}/{name}")
    return removed


def delete_model(target: Target, kind: str, name: str, *, timeout: float = DELETE_TIMEOUT,
                 interval: float = POLL_INTERVAL) -> None:
    """Delete the model and wait until it is gone; for a journey model, also what #194 leaves behind."""
    _delete_custom(target, (GROUP, VERSION, _plural(kind)), name, timeout=timeout, interval=interval,
                   what=f"{kind} {target.namespace}/{name}")
    if name.startswith(JOURNEY_PREFIX):  # issue #194 workaround; remove when #194 is fixed
        leftovers = _issue_194_leftovers(target, lambda model: model == name)
        _remove_issue_194_leftovers(target, leftovers, timeout=timeout, interval=interval)


def sweep_stale_models(target: Target, *, held: Collection[str] = (), timeout: float = DELETE_TIMEOUT,
                       interval: float = POLL_INTERVAL) -> SweepResult:
    """Delete journey models a crashed run left behind, except those named in `held`.

    Two guards, as in nebari_journeys' sweeps: the test-journey label and the
    journey- prefix. A labelled model without the prefix is reported as
    skipped. Then the issue #194 leftovers of any journey model that no
    longer exists are deleted too. Never raises on a failed delete; it is
    recorded in `failed`.
    """
    result = SweepResult()
    existing: set[str] = set()
    unlisted: list[str] = []
    for kind, plural in PLURALS.items():
        try:
            items = target.cluster.custom.list_namespaced_custom_object(
                group=GROUP, version=VERSION, namespace=target.namespace, plural=plural
            ).get("items", [])
        except ApiException as error:
            result.failed.append(f"<listing {plural} failed: {_describe(error)}>")
            unlisted.append(plural)
            continue
        for obj in items:
            name = obj["metadata"]["name"]
            labels = obj["metadata"].get("labels") or {}
            if labels.get(JOURNEY_LABEL) != "true" or name in held:
                existing.add(name)
                continue
            if not name.startswith(JOURNEY_PREFIX):
                existing.add(name)
                result.skipped.append(f"{kind}/{name}")
                continue
            try:
                _delete_custom(target, (GROUP, VERSION, plural), name, timeout=timeout, interval=interval,
                               what=f"{kind} {target.namespace}/{name}")
            except (ApiException, TimeoutError) as error:
                existing.add(name)
                result.failed.append(f"{kind}/{name}: {_describe(error)}")
            else:
                result.deleted.append(f"{kind}/{name}")

    # Issue #194 workaround (remove when #194 is fixed): routes, policies and
    # pools of journey models that no longer exist, including those deleted now.
    # When a model kind could not be listed, a live model of that kind cannot
    # be told from a deleted one, so this step is skipped for the whole sweep.
    if unlisted:
        log.warning("sweep: not removing issue #194 leftovers: %s could not be listed, so a live journey "
                    "model's routes, policies and pools could not be told from a deleted one's", ", ".join(unlisted))
        return result
    try:
        leftovers = _issue_194_leftovers(target, lambda model: model not in existing and model not in held)
    except ApiException as error:
        result.failed.append(f"<listing issue #194 leftovers failed: {_describe(error)}>")
        return result
    for kind, name in leftovers:
        try:
            result.deleted += _remove_issue_194_leftovers(target, [(kind, name)], timeout=timeout, interval=interval)
        except (ApiException, TimeoutError) as error:
            result.failed.append(f"{kind}/{name}: {_describe(error)}")
    return result


def held_by_release_state(path: Path) -> tuple[frozenset[str], frozenset[str]]:
    """(model names, usernames) an F13 state file holds; empty when there is no file (R6-F9).

    A malformed file raises (read_state names the path): the sweep does not
    guess what it may delete.
    """
    if not Path(path).exists():
        return frozenset(), frozenset()
    state = read_state(path)
    return frozenset({state.model_name}), frozenset({state.username})


class _HoldingKeycloak:
    """The Keycloak calls sweep_scratch_users makes, with the held users left out of the listing."""

    def __init__(self, keycloak, held: Collection[str]):
        self._keycloak = keycloak
        self._held = held

    def users(self, search: str) -> list[dict]:
        return [u for u in self._keycloak.users(search) if u.get("username") not in self._held]

    def delete_user(self, user_id: str) -> None:
        self._keycloak.delete_user(user_id)


def sweep_leftovers(target: Target, state_path: Path | None = None, *, timeout: float = DELETE_TIMEOUT,
                    interval: float = POLL_INTERVAL) -> dict[str, SweepResult]:
    """The session sweep: stale journey models and scratch users, sparing what the F13 state file holds.

    `state_path` is the --state path, else DEFAULT_STATE_PATH.
    """
    held_models, held_users = held_by_release_state(state_path or DEFAULT_STATE_PATH)
    if held_models or held_users:
        log.info("sweep: keeping %s and user %s for the F13 after stage", sorted(held_models), sorted(held_users))
    return {
        "models": sweep_stale_models(target, held=held_models, timeout=timeout, interval=interval),
        "users": sweep_scratch_users(_HoldingKeycloak(target.keycloak, held_users)),
    }
