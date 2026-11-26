"""The nebari_journeys helpers this suite uses, at the installed NIC tag (Q46, Q89).

nebari_journeys is pip-installed into the pixi environment by
install-helpers.sh, outside the lock. These tests need no cluster. They fail,
never skip, when the package is missing: a suite run without the helpers
cannot run a journey.
"""

import importlib
import inspect
import json
from importlib import metadata
from pathlib import Path

import pytest

HELPERS_JSON = Path(__file__).resolve().parent.parent / "results" / "helpers.json"
INSTALL_HINT = "run tests/journeys/install-helpers.sh <tag> first"


def _installed_tag() -> str:
    """The tag install-helpers.sh recorded, for failure messages."""
    try:
        data = json.loads(HELPERS_JSON.read_text())
        return f"{data['tag']} ({data['commit']})"
    except (OSError, ValueError, KeyError, TypeError):
        return f"unknown: {HELPERS_JSON} is missing or unreadable"


def _module(name: str):
    try:
        return importlib.import_module(name)
    except ImportError as e:
        error = e
    pytest.fail(f"{name} is not installed ({error}); {INSTALL_HINT}", pytrace=False)


def _params(func, *, public_only: bool = False) -> list[str]:
    """Parameter names; with public_only, without the private (_-prefixed) ones."""
    names = list(inspect.signature(func).parameters)
    return [name for name in names if not name.startswith("_")] if public_only else names


def _installer(dist: metadata.Distribution) -> str:
    return (dist.read_text("INSTALLER") or "").strip()


def test_only_nebari_journeys_installed_by_pip():
    """Q89: pixi list cannot see pip installs, so read each distribution's INSTALLER."""
    by_pip = sorted({dist.metadata["Name"] for dist in metadata.distributions() if _installer(dist) == "pip"})
    hint = "" if "nebari_journeys" in by_pip else f"; {INSTALL_HINT}"
    assert by_pip == ["nebari_journeys"], (
        f"pip installed {by_pip or 'nothing'}; only nebari_journeys may come from pip, "
        f"everything else from pixi.lock{hint}"
    )


# (module, attribute path, expected parameter names). These are the helpers
# the suite calls; a NIC release that changes one fails here, before any
# journey runs.
SIGNATURES = [
    ("nebari_journeys.keycloak", "Keycloak.create_user", ["self", "username", "password", "groups"]),
    ("nebari_journeys.keycloak", "Keycloak.delete_user", ["self", "user_id"]),
    ("nebari_journeys.keycloak", "Keycloak.add_user_to_group", ["self", "user_id", "group_name"]),
    ("nebari_journeys.keycloak", "Keycloak.group_id", ["self", "name"]),
    ("nebari_journeys.keycloak", "Keycloak.users", ["self", "search"]),
    # This row alone is checked on public parameters (PUBLIC_ONLY): the suite
    # never passes the private _session, _token, ... fields, so a change to
    # them is not a break.
    ("nebari_journeys.keycloak", "Keycloak", ["base_url", "password", "verify", "username"]),
    ("nebari_journeys.keycloak", "Keycloak.for_cluster", ["cluster", "domain", "verify"]),
    ("nebari_journeys.cluster", "Cluster", ["core", "custom", "storage"]),
    ("nebari_journeys.cluster", "Cluster.gateway_address", ["self"]),
    ("nebari_journeys.cluster", "Cluster.domain", ["self"]),
    ("nebari_journeys.cluster", "Cluster.api_host", ["self"]),
    ("nebari_journeys.cluster", "Cluster.secret_value", ["self", "namespace", "name", "key"]),
    ("nebari_journeys.trust", "spki_sha256_b64", ["pem"]),
    (
        "nebari_journeys.trust",
        "chromium_args",
        ["domain", "address", "mapping_needed", "trust_anchor_pem", "anchor_trust"],
    ),
    ("nebari_journeys.trust", "install_dns_mapping", ["domain", "address", "exempt_hosts"]),
    ("nebari_journeys.sweep", "SweepResult", ["deleted", "skipped", "failed"]),
    # The session sweep (llm_journeys.models.sweep_leftovers) and the
    # scratch_user fixture.
    ("nebari_journeys.keycloak", "sweep_scratch_users", ["keycloak"]),
    ("nebari_journeys.keycloak", "scratch_username", []),
    ("nebari_journeys.keycloak", "generated_password", []),
]

# Rows checked on public parameters only: dataclasses whose private fields
# the suite never passes. Every other row pins the full parameter list.
PUBLIC_ONLY = {("nebari_journeys.keycloak", "Keycloak")}


@pytest.mark.parametrize(("module", "attr", "expected"), SIGNATURES, ids=[f"{m}.{a}" for m, a, _ in SIGNATURES])
def test_helper_signature(module: str, attr: str, expected: list[str]):
    obj = _module(module)
    for part in attr.split("."):
        if not hasattr(obj, part):
            pytest.fail(f"{module}.{attr} is missing in nebari_journeys {_installed_tag()}", pytrace=False)
        obj = getattr(obj, part)
    actual = _params(obj, public_only=(module, attr) in PUBLIC_ONLY)
    assert actual == expected, (
        f"{module}.{attr} parameters changed in nebari_journeys {_installed_tag()}: "
        f"expected {expected}, got {actual}"
    )


def test_params_is_strict_except_for_the_keycloak_dataclass():
    """C5: a private parameter counts for every row but Keycloak, a dataclass with private fields."""
    def helper(self, user_id, _retry=None):
        pass

    assert _params(helper) == ["self", "user_id", "_retry"]
    assert _params(helper, public_only=True) == ["self", "user_id"]
    assert PUBLIC_ONLY == {("nebari_journeys.keycloak", "Keycloak")}
