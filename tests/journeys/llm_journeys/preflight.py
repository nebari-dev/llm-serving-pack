"""Is the cluster profile complete? One named cause instead of N feature failures.

preflight(target) returns the profile problems, empty when the profile is
complete. A missing pack namespace is not a problem but a note, "pack not
installed: see F1" (Q84): F1 reports why, and the tests that need the pack
fail on their own.

`python -m llm_journeys.preflight --target kind-dev --kube-context <ctx>` is
`make profile-check` (R5-F4): exit 0 when the profile is complete, 1 when it
is not (a note counts), 2 for a refused context or bad arguments.
"""

import argparse
import base64
import json
import logging
import sys
import tempfile
from collections.abc import Callable, Sequence
from pathlib import Path

import requests
from kubernetes.client.rest import ApiException
from nebari_journeys import constants

from llm_journeys import target as tgt
from llm_journeys.tokens import PasswordGrant

log = logging.getLogger(__name__)

HELPERS_JSON = Path(__file__).resolve().parent.parent / "results" / "helpers.json"
PACK_NOT_INSTALLED = "pack not installed: see F1"
INTERNAL_GATEWAY_ENV = "LLM_INTERNAL_GATEWAY_NAME"
CTP = ("gateway.envoyproxy.io", "v1alpha1", "clienttrafficpolicies")

# kind-dev demo user for the issuer check (dev/demo-users.sh keeps the
# passwords in Secret keycloak/demo-users).
DEMO_USERS_SECRET = ("keycloak", "demo-users")
DEMO_USER = "alice"

# A key-manager in dev mode (LLM_DEV_MODE=true, as make deploy leaves it)
# answers without a token; out of dev mode a request without one is 401.
KEY_MANAGER_MODELS_PATH = "/api/models"
UNAUTHORIZED = 401
REQUEST_TIMEOUT = 30


def _port_open(host: str, port: int) -> bool:
    return tgt._port_answers(port, host=host, timeout=3.0)


def _mint_demo_token(target: tgt.Target) -> str:
    """A password-grant token for the demo user; the password is never in a message."""
    password = target.cluster.secret_value(*DEMO_USERS_SECRET, DEMO_USER)
    return PasswordGrant(target.token_endpoint, target.password_grant_client).token(DEMO_USER, password)


def _key_manager_status(target: tgt.Target) -> int:
    """The status of one GET /api/models on the key-manager with no Authorization header."""
    return requests.get(
        f"{target.key_manager.base_url}{KEY_MANAGER_MODELS_PATH}",
        verify=target.key_manager.verify,
        timeout=REQUEST_TIMEOUT,
    ).status_code


def _issuer(token: str) -> str:
    payload = token.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))["iss"]


def _helper_tag(path: Path) -> str | None:
    try:
        return json.loads(path.read_text())["tag"]
    except (OSError, ValueError, KeyError, TypeError):
        return None


def _gateway_wide(policy: dict) -> bool:
    spec = policy.get("spec") or {}
    refs = list(spec.get("targetRefs") or []) + ([spec["targetRef"]] if spec.get("targetRef") else [])
    return any(
        ref.get("kind") == "Gateway" and ref.get("name") == constants.GATEWAY_NAME and not ref.get("sectionName")
        for ref in refs
    )


def check(
    target: tgt.Target,
    *,
    release_stage: str | None = None,
    apps=None,
    mint_token: Callable[[tgt.Target], str] | None = None,
    port_open: Callable[[str, int], bool] | None = None,
    helpers_json: Path | None = None,
    key_manager_status: Callable[[tgt.Target], int] | None = None,
) -> tuple[list[str], list[str]]:
    """(problems, notes) for the target's profile; checks only what its mode installs (Q79)."""
    problems: list[str] = []
    notes: list[str] = []

    record = tgt.read_provision_record(target.cluster)
    if not record:
        problems.append(
            f"provisioning record {tgt.JOURNEYS_NAMESPACE}/{tgt.RECORD_NAME} is missing: "
            "run make -C dev integration-up (kind-dev) or make -C dev nebari-pack (nebari)"
        )
    elif not target.mock_vllm_image:
        problems.append(f"the provisioning record has no image for {tgt.MOCK_PROVIDER_SELECTOR}")

    if target.provision_mode.startswith("release:") and release_stage != "before":
        problems.append(
            f"the profile was provisioned in mode {target.provision_mode}, "
            "which is valid only with --release-stage=before"
        )

    if target.nic_tag:
        path = helpers_json or HELPERS_JSON
        installed = _helper_tag(path)
        fix = f"run tests/journeys/install-helpers.sh {target.nic_tag}"
        if installed is None:
            problems.append(
                f"cannot tell which nebari_journeys tag is installed: {path} is missing or unreadable; {fix}"
            )
        elif installed != target.nic_tag:
            problems.append(
                f"nebari_journeys is installed at {installed} but the cluster runs NIC {target.nic_tag}: {fix}"
            )

    port = target.gateway_https_port
    if not (port_open or _port_open)(tgt.GATEWAY_HOST_ADDRESS, port):
        problems.append(
            f"nothing answers on {tgt.GATEWAY_HOST_ADDRESS}:{port}, the gateway's host port: if the cluster was "
            "created before dev/kind-config.yaml (no host port mapping), recreate the cluster: "
            "make -C dev teardown setup"
        )

    if not target.pack_installed:
        notes.append(PACK_NOT_INSTALLED)
    else:
        apps = apps if apps is not None else tgt._apps(target.cluster)
        where = f"{target.namespace}/{target.operator_deployment}"
        try:
            setting = tgt.deployment_env(apps, target.namespace, target.operator_deployment, INTERNAL_GATEWAY_ENV)
        except ApiException as error:
            if error.status != tgt.NOT_FOUND:
                raise
            problems.append(f"operator Deployment {where} not found")
        else:
            if setting != target.internal_gateway_name:
                problems.append(
                    f"operator Deployment {where} has {INTERNAL_GATEWAY_ENV}={setting}, "
                    f"want {target.internal_gateway_name} "
                    "(an interrupted F11 run, or make deploy: run make -C dev integration-up)"
                )

    if target.expects_gateway_wide_policy:
        group, version, plural = CTP
        try:
            policies = target.cluster.custom.list_namespaced_custom_object(
                group=group, version=version, namespace=constants.GATEWAY_NAMESPACE, plural=plural
            ).get("items", [])
        except ApiException as error:
            if error.status != tgt.NOT_FOUND:
                raise
            policies = None
            problems.append(
                f"the ClientTrafficPolicy CRD ({group}/{version}) is not installed, so there is no Gateway-wide "
                f"policy on {constants.GATEWAY_NAMESPACE}/{constants.GATEWAY_NAME} (run make -C dev integration-up)"
            )
        if policies is not None and not any(_gateway_wide(p) for p in policies):
            problems.append(
                f"no Gateway-wide ClientTrafficPolicy on {constants.GATEWAY_NAMESPACE}/{constants.GATEWAY_NAME} "
                "(the kind-dev profile installs one: run make -C dev integration-up)"
            )

    # R6-F7: only where tokens come from a password grant; on a browser-login
    # target F3 checks the captured token's iss instead. The same targets get
    # the key-manager from make -C dev, so it may be left in dev mode.
    if target.password_grant_client is not None:
        try:
            iss = _issuer((mint_token or _mint_demo_token)(target))
        except (RuntimeError, requests.RequestException, ApiException, KeyError, ValueError, IndexError) as error:
            # The message names the cause only; it never carries the password.
            cause = f"ApiException {error.status} {error.reason}" if isinstance(error, ApiException) else str(error)
            problems.append(f"cannot mint a token by password grant to check the issuer: {cause}")
            iss = None
        if iss is not None and iss != target.issuer_url:
            problems.append(
                f"a token minted by password grant has iss {iss}, not {target.issuer_url} "
                "(Keycloak's KC_HOSTNAME pin is missing: run make -C dev integration-up)"
            )
        try:
            status = (key_manager_status or _key_manager_status)(target)
        except requests.RequestException as error:
            problems.append(
                f"cannot reach the key-manager at {target.key_manager.base_url} to check that it requires a token: "
                f"{type(error).__name__}: {error}"
            )
        else:
            if status != UNAUTHORIZED:
                problems.append(
                    f"key-manager accepts requests without a token (dev mode; GET {KEY_MANAGER_MODELS_PATH} "
                    f"answered HTTP {status}, want {UNAUTHORIZED}): run make -C dev integration-up"
                )

    return problems, notes


def preflight(target: tgt.Target, **seams) -> list[str]:
    """Profile problems, empty when the profile is complete; notes are logged, not returned."""
    problems, notes = check(target, **seams)
    for note in notes:
        log.warning("preflight: %s", note)
    return problems


def main(argv: Sequence[str]) -> int:
    parser = argparse.ArgumentParser(prog="python -m llm_journeys.preflight", description=__doc__.splitlines()[0])
    parser.add_argument("--target", choices=("kind-dev", "nebari"), required=True)
    parser.add_argument("--kube-context", required=True)
    parser.add_argument("--gateway-https-port", type=int, default=None)
    args = parser.parse_args(argv)
    try:
        tgt.require_local_kind_context(args.kube_context)
    except tgt.UnsafeContext as error:
        print(f"profile-check: {error}", file=sys.stderr)
        return 2
    try:
        with (
            tgt.connect_cluster(args.kube_context) as cluster,
            tempfile.TemporaryDirectory(prefix="llm-journeys-preflight-") as workdir,
            tgt.open_target(
                args.target, args.kube_context, cluster, Path(workdir), gateway_https_port=args.gateway_https_port
            ) as target,
        ):
            problems, notes = check(target)
    except Exception as error:  # noqa: BLE001 - any failure to open the target means the profile is not up
        print(f"profile-check: cannot open target {args.target} on {args.kube_context}: {error}", file=sys.stderr)
        return 1
    for line in problems:
        print(f"PROBLEM: {line}", file=sys.stderr)
    for line in notes:
        print(f"NOTE: {line}", file=sys.stderr)
    if problems or notes:
        return 1
    print(f"profile-check: {args.target} profile on {args.kube_context} is complete")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
