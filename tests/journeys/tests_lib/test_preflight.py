"""llm_journeys.preflight: an incomplete profile is named once, with its cause.

No cluster: in-memory kube APIs (tests_lib/fakes.py) and injected token,
port and helper-file seams.
"""

import base64
import json

import pytest
from kubernetes.client.rest import ApiException

from llm_journeys import preflight as pf
from llm_journeys.target import REALM_TOKEN_PATH, Endpoint
from tests_lib.fakes import (
    FakeApps,
    FakeCore,
    FakeCustom,
    fake_cluster,
    http_fake,
    make_target,
    nebari_shaped,
    record_data,
)

ISSUER = "http://keycloak.keycloak.svc.cluster.local:8080/realms/nebari"
GATEWAY_WIDE_POLICY = {
    "metadata": {"name": "nebari-gateway-client-traffic"},
    "spec": {
        "targetRefs": [{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "nebari-gateway"}],
        "connection": {"bufferLimit": "50Mi"},
    },
}
LISTENER_POLICY = {
    "metadata": {"name": "listener-only"},
    "spec": {"targetRefs": [{"kind": "Gateway", "name": "nebari-gateway", "sectionName": "llm-https"}]},
}
CTP_KEY = ("gateway.envoyproxy.io", "envoy-gateway-system", "clienttrafficpolicies")


def token_with(iss: str) -> str:
    def part(obj):
        return base64.urlsafe_b64encode(json.dumps(obj).encode()).decode().rstrip("=")

    return f"{part({'alg': 'RS256'})}.{part({'iss': iss, 'sub': 'alice'})}.signature"


def profile(*, record=True, policies=(GATEWAY_WIDE_POLICY,), **target_fields):
    """A Target whose fake cluster holds a complete kind-dev profile, unless told otherwise."""
    configmaps = {("llm-journeys", "provision"): record_data()} if record else {}
    cluster = fake_cluster(FakeCore(configmaps=configmaps), FakeCustom({CTP_KEY: list(policies)}))
    return make_target(cluster=cluster, **target_fields)


def operator_env(value="nebari-gateway"):
    return FakeApps({("llm-operator-system", "llm-operator"): {"LLM_INTERNAL_GATEWAY_NAME": value}})


def never(*args, **kwargs):
    pytest.fail("called where it must not be")


def run(target, **overrides):
    seams = dict(
        apps=operator_env(),
        mint_token=lambda t: token_with(ISSUER),
        port_open=lambda host, port: True,
        helpers_json=None,
        key_manager_status=lambda t: 401,
    )
    seams.update(overrides)
    return pf.check(target, **seams)


def test_preflight_complete_profile_has_no_problems():
    assert run(profile()) == ([], [])
    seams = dict(apps=operator_env(), mint_token=lambda t: token_with(ISSUER), port_open=lambda h, p: True,
                 key_manager_status=lambda t: 401)
    assert pf.preflight(profile(), **seams) == []


def test_preflight_missing_record():
    problems, _ = run(profile(record=False))
    assert len(problems) == 1
    assert "provisioning record llm-journeys/provision is missing" in problems[0]
    assert "make -C dev integration-up" in problems[0]


def test_preflight_missing_pack_namespace_is_info(caplog):
    caplog.set_level("INFO")
    target = profile(pack_installed=False)
    problems, notes = run(target, apps=FakeApps())
    assert problems == []
    assert notes == ["pack not installed: see F1"]
    assert pf.preflight(target, apps=FakeApps(), mint_token=lambda t: token_with(ISSUER), port_open=lambda h, p: True,
                        key_manager_status=lambda t: 401) == []
    assert "pack not installed: see F1" in caplog.text


def test_preflight_names_missing_port_mapping():
    seen = []

    def closed(host, port):
        seen.append((host, port))
        return False

    problems, _ = run(profile(gateway_https_port=48555), port_open=closed)
    assert seen == [("127.0.0.1", 48555)]
    assert len(problems) == 1
    assert "\n" not in problems[0]
    assert "127.0.0.1:48555" in problems[0]
    assert "recreate the cluster" in problems[0]


def test_preflight_issuer_check_only_with_password_grant():
    problems, _ = run(profile(), mint_token=lambda t: token_with("http://localhost:8180/realms/nebari"))
    assert problems == [
        f"a token minted by password grant has iss http://localhost:8180/realms/nebari, not {ISSUER} "
        "(Keycloak's KC_HOSTNAME pin is missing: run make -C dev integration-up)"
    ]

    nebari = nebari_shaped(cluster=profile().cluster, issuer_url="https://keycloak.nebari.test/realms/nebari")
    problems, _ = run(nebari, mint_token=never)
    assert not [p for p in problems if "iss" in p]


def test_preflight_internal_gateway_setting_mismatch():
    problems, _ = run(profile(), apps=operator_env("journey-no-such-gateway"))
    assert problems == [
        "operator Deployment llm-operator-system/llm-operator has LLM_INTERNAL_GATEWAY_NAME=journey-no-such-gateway, "
        "want nebari-gateway (an interrupted F11 run, or make deploy: run make -C dev integration-up)"
    ]
    problems, _ = run(profile(), apps=FakeApps())
    assert problems == ["operator Deployment llm-operator-system/llm-operator not found"]


def test_preflight_release_mode_only_with_before_stage():
    release = profile(provision_mode="release:0.2.0")
    problems, _ = run(release)
    assert problems == [
        "the profile was provisioned in mode release:0.2.0, which is valid only with --release-stage=before"
    ]
    assert run(release, release_stage="before") == ([], [])
    assert run(release, release_stage="after")[0] == problems
    assert run(profile(), release_stage="before") == ([], [])


def test_preflight_policy_only_where_expected():
    problems, _ = run(profile(policies=[LISTENER_POLICY]))
    assert problems == [
        "no Gateway-wide ClientTrafficPolicy on envoy-gateway-system/nebari-gateway "
        "(the kind-dev profile installs one: run make -C dev integration-up)"
    ]


def test_preflight_policy_not_required_on_nebari():
    target = nebari_shaped(cluster=profile(policies=[]).cluster)
    problems, _ = run(target, mint_token=never)
    assert not [p for p in problems if "ClientTrafficPolicy" in p]


def test_preflight_helper_tag_mismatch(tmp_path):
    helpers = tmp_path / "helpers.json"
    helpers.write_text(json.dumps({"tag": "v0.17.0", "commit": "c" * 40}))
    problems, _ = run(profile(nic_tag="v0.18.0"), helpers_json=helpers)
    assert problems == [
        "nebari_journeys is installed at v0.17.0 but the cluster runs NIC v0.18.0: "
        "run tests/journeys/install-helpers.sh v0.18.0"
    ]

    helpers.write_text(json.dumps({"tag": "v0.18.0", "commit": "c" * 40}))
    assert run(profile(nic_tag="v0.18.0"), helpers_json=helpers) == ([], [])

    problems, _ = run(profile(nic_tag="v0.18.0"), helpers_json=tmp_path / "absent.json")
    assert problems == [
        f"cannot tell which nebari_journeys tag is installed: {tmp_path / 'absent.json'} is missing or "
        "unreadable; run tests/journeys/install-helpers.sh v0.18.0"
    ]

    assert run(profile(nic_tag=None), helpers_json=tmp_path / "absent.json") == ([], [])  # kind-dev: no NIC


def test_main_refuses_non_kind_context(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("KUBECONFIG", str(tmp_path / "none"))
    assert pf.main(["--target", "kind-dev", "--kube-context", "prod"]) == 2
    assert "refusing kube context 'prod'" in capsys.readouterr().err


# --- fix round 1 (M1, M7) ---------------------------------------------------


def test_preflight_missing_ctp_crd_is_a_problem():
    class NoCrd(FakeCustom):
        def list_namespaced_custom_object(self, group, version, namespace, plural):
            raise ApiException(status=404, reason="Not Found")

    target = make_target(cluster=fake_cluster(FakeCore(configmaps={("llm-journeys", "provision"): record_data()}), NoCrd()))
    problems, _ = run(target)
    assert problems == [
        "the ClientTrafficPolicy CRD (gateway.envoyproxy.io/v1alpha1) is not installed, so there is no Gateway-wide "
        "policy on envoy-gateway-system/nebari-gateway (run make -C dev integration-up)"
    ]


def test_preflight_mint_failure_is_a_problem():
    def cannot(target):
        raise RuntimeError("password grant for alice returned HTTP 401")

    problems, _ = run(profile(), mint_token=cannot)
    assert problems == [
        "cannot mint a token by password grant to check the issuer: password grant for alice returned HTTP 401"
    ]


def test_preflight_default_mint_is_password_grant_and_hides_password():
    """No mint_token seam: the demo user's password grant runs against a fake token endpoint."""
    password = "demo-alice-pa55"
    configmaps = {("llm-journeys", "provision"): record_data()}
    secrets = {("keycloak", "demo-users"): {"alice": password}}
    core = FakeCore(configmaps=configmaps, secrets=secrets)
    cluster = fake_cluster(core, FakeCustom({CTP_KEY: [GATEWAY_WIDE_POLICY]}))
    with http_fake(lambda request: (401, {"error": "invalid_grant"})) as (base_url, received):
        problems, _ = run(make_target(cluster=cluster, token_endpoint=Endpoint(base_url, True)), mint_token=None)
    assert [r.path for r in received] == [REALM_TOKEN_PATH]
    assert password.encode() in received[0].body
    [problem] = problems
    assert problem.startswith("cannot mint a token by password grant to check the issuer: ")
    assert "HTTP 401" in problem
    assert password not in problem


def test_preflight_port_check_reuses_port_answers(monkeypatch):
    seen = []
    monkeypatch.setattr(pf.tgt, "_port_answers", lambda port, **kw: seen.append(port) or True)
    problems, _ = pf.check(
        profile(gateway_https_port=48777), apps=operator_env(), mint_token=lambda t: token_with(ISSUER),
        key_manager_status=lambda t: 401,
    )
    assert problems == []
    assert seen == [48777]


# --- Slice 6 checkpoint fix (C4): a key-manager in dev mode ------------------


@pytest.mark.parametrize(
    ("status", "want"),
    [
        (401, []),
        (200, ["key-manager accepts requests without a token (dev mode; GET /api/models answered HTTP 200, "
               "want 401): run make -C dev integration-up"]),
    ],
    ids=["auth-on", "dev-mode"],
)
def test_preflight_key_manager_must_refuse_a_missing_token(status, want):
    """No key_manager_status seam: one unauthenticated GET /api/models against a fake key-manager."""
    with http_fake(lambda request: (status, {"models": []})) as (base_url, received):
        problems, _ = run(profile(key_manager=Endpoint(base_url, True)), key_manager_status=None)
    assert [(r.method, r.path, r.header_values("Authorization")) for r in received] == [("GET", "/api/models", [])]
    assert problems == want


def test_preflight_key_manager_check_only_with_password_grant():
    nebari = nebari_shaped(cluster=profile().cluster)
    problems, _ = run(nebari, mint_token=never, key_manager_status=never)
    assert not [p for p in problems if "key-manager" in p]


def test_preflight_unreachable_key_manager_is_a_problem():
    with http_fake(lambda request: (200, {})) as (base_url, _):
        pass  # closed again: nothing listens on base_url now
    problems, _ = run(profile(key_manager=Endpoint(base_url, True)), key_manager_status=None)
    [problem] = problems
    assert problem.startswith(f"cannot reach the key-manager at {base_url} to check that it requires a token: ")
