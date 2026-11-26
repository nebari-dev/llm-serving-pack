"""llm_journeys.target: kube context safety, port-forwards, trust, Target, the lease.

No cluster: a fake kubeconfig file, a fake kubectl on PATH, and in-memory
kube APIs (tests_lib/fakes.py).
"""

import json
import os
import socket
import sys
import time
from pathlib import Path
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta

import pytest
import yaml
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
from kubernetes import client
from kubernetes.client.rest import ApiException
from nebari_journeys import trust

from llm_journeys import target as tgt
from tests_lib.fakes import FakeApps, FakeCore, fake_cluster, make_target, nebari_shaped, not_found, record_data

# --- kube context safety ----------------------------------------------------

CONTEXTS = {
    # name: API server
    "kind-llm-serving-test": "https://127.0.0.2:6443",
    "kind-other": "https://localhost:7443",
    "kind-remote": "https://10.20.30.40:6443",
    "prod-loopback": "https://127.0.0.1:6443",
}


@pytest.fixture
def kubeconfig(tmp_path, monkeypatch):
    """A kubeconfig with several contexts; the current one is kind-other."""
    path = tmp_path / "kubeconfig"
    path.write_text(
        yaml.safe_dump(
            {
                "apiVersion": "v1",
                "kind": "Config",
                "current-context": "kind-other",
                "clusters": [{"name": n, "cluster": {"server": s}} for n, s in CONTEXTS.items()],
                "users": [{"name": n, "user": {"token": f"token-{n}"}} for n in CONTEXTS],
                "contexts": [{"name": n, "context": {"cluster": n, "user": n}} for n in CONTEXTS],
            }
        )
    )
    monkeypatch.setenv("KUBECONFIG", str(path))
    # connect_cluster sets the process-wide default configuration; put it back.
    monkeypatch.setattr(client.Configuration, "_default", None)
    return path


def test_refuses_non_kind_context(kubeconfig):
    with pytest.raises(tgt.UnsafeContext, match="prod-loopback"):
        tgt.require_local_kind_context("prod-loopback")
    with pytest.raises(tgt.UnsafeContext, match="prod-loopback"):
        with tgt.connect_cluster("prod-loopback"):
            pass


def test_refuses_non_loopback_server(kubeconfig):
    with pytest.raises(tgt.UnsafeContext, match=r"kind-remote.*10\.20\.30\.40.*not on a loopback"):
        tgt.require_local_kind_context("kind-remote")


def test_refuses_loopback_server_behind_a_proxy(kubeconfig):
    """A loopback server reached through proxy-url may be any cluster (C3)."""
    config = yaml.safe_load(kubeconfig.read_text())
    for entry in config["clusters"]:
        if entry["name"] == "kind-llm-serving-test":
            entry["cluster"]["proxy-url"] = "http://proxy.example.com:3128"
    kubeconfig.write_text(yaml.safe_dump(config))
    with pytest.raises(tgt.UnsafeContext, match=r"kind-llm-serving-test.*proxy http://proxy\.example\.com:3128"):
        tgt.require_local_kind_context("kind-llm-serving-test")
    with pytest.raises(tgt.UnsafeContext, match="proxy"):
        with tgt.connect_cluster("kind-llm-serving-test"):
            pass
    tgt.require_local_kind_context("kind-other")  # other contexts in the file are unaffected


def test_refuses_unknown_context(kubeconfig):
    with pytest.raises(tgt.UnsafeContext, match="kind-missing"):
        tgt.require_local_kind_context("kind-missing")


def test_accepts_local_kind_context(kubeconfig):
    tgt.require_local_kind_context("kind-llm-serving-test")
    tgt.require_local_kind_context("kind-other")  # localhost counts as loopback


def test_uses_named_context_not_current(kubeconfig):
    with tgt.connect_cluster("kind-llm-serving-test") as cluster:
        assert cluster.core.api_client.configuration.host == "https://127.0.0.2:6443"
        assert client.Configuration.get_default_copy().host == "https://127.0.0.2:6443"


def test_current_context_unchanged(kubeconfig):
    before = kubeconfig.read_bytes()
    tgt.require_local_kind_context("kind-llm-serving-test")
    with tgt.connect_cluster("kind-llm-serving-test"):
        pass
    assert kubeconfig.read_bytes() == before


def test_cluster_api_host_is_named_context(kubeconfig):
    """R5-F3: Cluster reads the in-process default, which is the named context."""
    with tgt.connect_cluster("kind-llm-serving-test") as cluster:
        assert cluster.api_host() == "127.0.0.2"


# --- port-forwards ---------------------------------------------------------

FAKE_KUBECTL = r"""#!{python}
# Fake kubectl: records its argv and pid, then serves the local port of a
# `port-forward ... <local>:<remote>` until terminated. With
# FAKE_KUBECTL_EXIT_FIRST set, the first run exits after half a second.
import json, os, re, socket, sys, time
with open(os.environ["FAKE_KUBECTL_LOG"], "a") as f:
    f.write(json.dumps({{"argv": sys.argv[1:], "pid": os.getpid()}}) + "\n")
local = next(int(a.split(":")[0]) for a in sys.argv[1:] if re.fullmatch(r"\d+:\d+", a))
srv = socket.socket()
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", local))
srv.listen()
print(f"Forwarding from 127.0.0.1:{{local}}", flush=True)
marker = os.environ.get("FAKE_KUBECTL_EXIT_FIRST")
if marker and not os.path.exists(marker):
    open(marker, "w").close()
    time.sleep(0.5)
    sys.exit(1)
srv.settimeout(0.2)
while True:
    try:
        conn, _ = srv.accept()
        conn.close()
    except socket.timeout:
        pass
"""


@pytest.fixture
def fake_kubectl(tmp_path, monkeypatch):
    bindir = tmp_path / "bin"
    bindir.mkdir()
    kubectl = bindir / "kubectl"
    kubectl.write_text(FAKE_KUBECTL.format(python=sys.executable))
    kubectl.chmod(0o755)
    logfile = tmp_path / "kubectl.log"
    logfile.touch()
    # Only the fake: a real kubectl must never be found, even when the fake
    # cannot run (the fake's shebang names this Python by absolute path).
    monkeypatch.setenv("PATH", str(bindir))
    monkeypatch.setenv("FAKE_KUBECTL_LOG", str(logfile))
    return logfile


def calls(logfile):
    return [json.loads(line) for line in logfile.read_text().splitlines()]


def alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


def answers(port: int) -> bool:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=1):
            return True
    except OSError:
        return False


WANTED = [("llm-operator-system", "llm-key-manager", 8080), ("keycloak", "keycloak", 8080)]


def test_port_forwards_pinned_to_context(fake_kubectl):
    with tgt.port_forwards("kind-llm-serving-test", WANTED) as forwards:
        assert [(f.namespace, f.service, f.remote_port) for f in forwards] == WANTED
        assert len({f.local_port for f in forwards}) == 2
        recorded = calls(fake_kubectl)  # the two processes log in either order
        assert len(recorded) == 2
        for forward in forwards:
            (argv,) = [c["argv"] for c in recorded if f"svc/{forward.service}" in c["argv"]]
            assert argv[:2] == ["--context", "kind-llm-serving-test"]
            assert argv[argv.index("-n") + 1] == forward.namespace
            assert "port-forward" in argv
            assert f"svc/{forward.service}" in argv
            assert f"{forward.local_port}:{forward.remote_port}" in argv
            assert argv[argv.index("--address") + 1] == "127.0.0.1"
            assert answers(forward.local_port)


def test_port_forward_restarted_when_it_exits(fake_kubectl, tmp_path, monkeypatch):
    monkeypatch.setenv("FAKE_KUBECTL_EXIT_FIRST", str(tmp_path / "exited-once"))
    with tgt.port_forwards("kind-llm-serving-test", WANTED[:1], restart_delay=0.1) as forwards:
        deadline = time.monotonic() + 10
        while len(calls(fake_kubectl)) < 2 and time.monotonic() < deadline:
            time.sleep(0.1)
        first, second = calls(fake_kubectl)[:2]
        assert first["argv"] == second["argv"]  # same context, service and local port
        assert first["pid"] != second["pid"]
        deadline = time.monotonic() + 10
        while not answers(forwards[0].local_port) and time.monotonic() < deadline:
            time.sleep(0.1)
        assert answers(forwards[0].local_port)


def test_port_forwards_stopped_after_failed_session(fake_kubectl):
    with pytest.raises(RuntimeError, match="the session failed"):
        with tgt.port_forwards("kind-llm-serving-test", WANTED):
            raise RuntimeError("the session failed")
    pids = [c["pid"] for c in calls(fake_kubectl)]
    assert len(pids) == 2
    assert not any(alive(pid) for pid in pids)


# --- per-endpoint trust -----------------------------------------------------


def self_signed_leaf(hostname: str) -> str:
    key = ec.generate_private_key(ec.SECP256R1())
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, hostname)])
    now = datetime.now(UTC)
    cert = (
        x509.CertificateBuilder()
        .subject_name(name)
        .issuer_name(name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=1))
        .not_valid_after(now + timedelta(days=1))
        .add_extension(x509.SubjectAlternativeName([x509.DNSName(hostname)]), critical=False)
        .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
        .sign(key, hashes.SHA256())
    )
    return cert.public_bytes(serialization.Encoding.PEM).decode()


def test_endpoint_from_secret_uses_own_ca(tmp_path):
    gateway, keys, leaf_only = (self_signed_leaf(h) for h in ("llm.local", "llm-keys.local", "kc.local"))
    cluster = fake_cluster(
        FakeCore(
            secrets={
                ("envoy-gateway-system", "gw"): {"ca.crt": gateway, "tls.crt": "other"},
                ("llm", "keys"): {"ca.crt": keys, "tls.crt": "other"},
                ("keycloak", "kc"): {"tls.crt": leaf_only},
            }
        )
    )
    a = tgt.endpoint_from_secret(cluster, "https://llm.local:48443", ("envoy-gateway-system", "gw"), tmp_path)
    b = tgt.endpoint_from_secret(cluster, "https://llm-keys.local", ("llm", "keys"), tmp_path)
    c = tgt.endpoint_from_secret(cluster, "https://kc.local", ("keycloak", "kc"), tmp_path)
    assert a.base_url == "https://llm.local:48443"
    assert Path(a.verify).read_text() == gateway
    assert Path(b.verify).read_text() == keys
    assert Path(c.verify).read_text() == leaf_only  # a self-signed leaf is its own anchor
    assert len({a.verify, b.verify, c.verify}) == 3
    with pytest.raises(RuntimeError, match="llm/missing"):
        tgt.endpoint_from_secret(cluster, "https://x.local", ("llm", "missing"), tmp_path)


def test_chromium_args_lists_every_pin_once():
    one, two = self_signed_leaf("llm.local"), self_signed_leaf("llm-keys.local")
    args = tgt.chromium_args(make_target(browser_pins=(one, two, one)))
    pins = [a for a in args if a.startswith("--ignore-certificate-errors-spki-list=")]
    assert pins == [f"--ignore-certificate-errors-spki-list={trust.spki_sha256_b64(one)},{trust.spki_sha256_b64(two)}"]
    assert "--host-resolver-rules=MAP *.local 127.0.0.1,MAP local 127.0.0.1" in args
    assert not [a for a in tgt.chromium_args(make_target(browser_pins=())) if "spki" in a]


# --- Target ---------------------------------------------------------------


def test_target_invariants():
    make_target()
    nebari_shaped()
    with pytest.raises(ValueError, match="exactly one of ui and password_grant_client"):
        make_target(ui=tgt.Endpoint("https://llm-keys.local", True))
    with pytest.raises(ValueError, match="exactly one of ui and password_grant_client"):
        nebari_shaped(ui=None)
    with pytest.raises(ValueError, match="usage_console and usage_admin_group"):
        nebari_shaped(usage_admin_group=None)
    with pytest.raises(ValueError, match="usage_console and usage_admin_group"):
        make_target(usage_admin_group="llm-usage-admins")
    with pytest.raises(ValueError, match="at most one of usage_console and openmeter"):
        nebari_shaped(openmeter=tgt.Endpoint("http://127.0.0.1:1", True))
    with pytest.raises(ValueError, match="only on kind-dev"):
        nebari_shaped(operator_reconfig_allowed=True)
    with pytest.raises(ValueError, match="never False"):
        tgt.Endpoint("https://llm.local", False)
    make_target(openmeter=None, openmeter_cluster_url=None)  # the Q32 benchmark run


def test_read_provision_record_maps_images():
    """Controller ruling 1: id -> image_id, repo_digests keep only sha256:<hex>."""
    images = {
        "app=llm-operator": {
            "reference": "llm-operator:dev",
            "id": "sha256:" + "1" * 64,
            "repo_digests": ["docker.io/library/import-2026-11-26@sha256:" + "2" * 64],
        }
    }
    data = record_data(deviations=["d1"], skipped=["openmeter"], images=images)
    cluster = fake_cluster(FakeCore(configmaps={("llm-journeys", "provision"): data}))
    record = tgt.read_provision_record(cluster)
    assert record["mode"] == "working-tree"
    assert record["nic_tag"] == ""
    assert record["deviations"] == ("d1",)
    assert record["skipped"] == ("openmeter",)
    assert record["images"] == {
        "app=llm-operator": tgt.ImageRecord("llm-operator:dev", "sha256:" + "1" * 64, ("sha256:" + "2" * 64,))
    }
    assert tgt.read_provision_record(fake_cluster()) == {}


def test_kind_dev_target_from_record(tmp_path, monkeypatch):
    leaf = self_signed_leaf("llm.local")
    core = FakeCore(
        secrets={("llm-operator-system", "nebari-llm-shared-tls"): {"ca.crt": leaf, "tls.crt": leaf}},
        configmaps={("llm-journeys", "provision"): record_data()},
        namespaces={"llm-operator-system", "llm-journeys"},
    )
    apps = FakeApps({("keycloak", "keycloak"): {"KC_BOOTSTRAP_ADMIN_PASSWORD": "kc-admin"}})
    monkeypatch.setattr(tgt, "_apps", lambda cluster: apps)
    forwards = [
        tgt.PortForward("llm-operator-system", "llm-key-manager", 8080, 41001),
        tgt.PortForward("keycloak", "keycloak", 8080, 41002),
    ]
    target = tgt.kind_dev_target("kind-llm-serving-test", fake_cluster(core), tmp_path, forwards, gateway_https_port=48555)
    assert target.external.base_url == "https://llm.local:48555"
    assert target.internal.base_url == "https://llm-internal.local:48555"
    assert Path(target.external.verify).read_text() == leaf
    assert target.browser_pins == (leaf,)
    assert target.key_manager == tgt.Endpoint("http://127.0.0.1:41001", True)
    assert target.token_endpoint == tgt.Endpoint("http://127.0.0.1:41002", True)
    assert target.keycloak.base_url == "http://127.0.0.1:41002"
    assert "kc-admin" not in repr(target)
    assert target.mock_vllm_image == "mock-vllm:dev"  # controller ruling 2
    assert target.expected_images["app=mock-provider"].repo_digests == ("sha256:" + "b" * 64,)
    assert target.openmeter is None and target.openmeter_cluster_url is None  # no OpenMeter forward
    assert target.pack_installed
    assert target.nic_tag is None


def test_kind_dev_target_without_pack(tmp_path, monkeypatch):
    core = FakeCore(configmaps={("llm-journeys", "provision"): record_data()}, namespaces={"llm-journeys"})
    monkeypatch.setattr(tgt, "_apps", lambda c: FakeApps({("keycloak", "keycloak"): {"KC_BOOTSTRAP_ADMIN_PASSWORD": "x"}}))
    forwards = [
        tgt.PortForward("llm-operator-system", "llm-key-manager", 8080, 41001),
        tgt.PortForward("keycloak", "keycloak", 8080, 41002),
        tgt.PortForward("usage-openmeter", "usage-openmeter-api", 80, 41003),
    ]
    target = tgt.kind_dev_target("kind-llm-serving-test", fake_cluster(core), tmp_path, forwards)
    assert not target.pack_installed
    assert target.external == tgt.Endpoint("https://llm.local:48443", True)
    assert target.openmeter == tgt.Endpoint("http://127.0.0.1:41003", True)
    assert target.openmeter_cluster_url == "http://usage-openmeter-api.usage-openmeter.svc"


def test_nebari_target_is_a_stub(tmp_path):
    with pytest.raises(NotImplementedError, match="Slice 12"):
        tgt.nebari_target("kind-nic", fake_cluster(), tmp_path)


def pod(name, labels, image, image_id, phase="Running"):
    return client.V1Pod(
        metadata=client.V1ObjectMeta(name=name, namespace="ns", labels=labels),
        spec=client.V1PodSpec(
            containers=[client.V1Container(name="sidecar", image="envoy:1"), client.V1Container(name="main", image=image)]
        ),
        status=client.V1PodStatus(
            phase=phase,
            container_statuses=[
                client.V1ContainerStatus(
                    name="sidecar", image="envoy:1", image_id="sha256:side", ready=True, restart_count=0
                ),
                client.V1ContainerStatus(name="main", image="sha256:x", image_id=image_id, ready=True, restart_count=0),
            ],
        ),
    )


def test_pod_images_reads_running_pods():
    pods = [
        pod("a-pending", {"app": "mock-provider"}, "mock-vllm:dev", "sha256:pending", phase="Pending"),
        pod("b-journey", {"app": "mock-provider", "nebari.dev/test-journey": "true"}, "mock-vllm:dev", "sha256:t"),
        pod("c-running", {"app": "mock-provider"}, "docker.io/library/mock-vllm:dev", "sha256:run"),
    ]
    target = make_target(cluster=fake_cluster(FakeCore(pods=pods)))
    assert tgt.pod_images(target) == {"app=mock-provider": ("docker.io/library/mock-vllm:dev", "sha256:run")}


def test_pod_images_matches_full_reference():
    """M5: a pod running some other image fails naming the selector; no fallback container."""
    pods = [pod("a", {"app": "mock-provider"}, "quay.io/nebari/vllm:latest", "sha256:other")]
    target = make_target(cluster=fake_cluster(FakeCore(pods=pods)))
    with pytest.raises(RuntimeError, match=r"app=mock-provider.*mock-vllm:dev"):
        tgt.pod_images(target)
    exact = [pod("a", {"app": "mock-provider"}, "mock-vllm:dev", "sha256:exact")]
    assert tgt.pod_images(make_target(cluster=fake_cluster(FakeCore(pods=exact)))) == {
        "app=mock-provider": ("mock-vllm:dev", "sha256:exact")
    }


# --- the session lease ------------------------------------------------------


class FakeCoordination:
    """One Lease, with resourceVersion checks like the API server's."""

    def __init__(self, lease=None):
        self.lease = lease
        self.version = 1
        if lease is not None:
            lease.metadata.resource_version = "1"
        self.writes = []
        self.kwargs = []  # extra keyword arguments of every call, such as _request_timeout

    def read_namespaced_lease(self, name, namespace, **kwargs):
        self.kwargs.append(("read", kwargs))
        if self.lease is None:
            raise not_found()
        return self.lease.model_copy(deep=True)

    def create_namespaced_lease(self, namespace, body, **kwargs):
        self.kwargs.append(("create", kwargs))
        if self.lease is not None:
            raise ApiException(status=409, reason="AlreadyExists")
        self._store("create", body)
        return self.lease

    def replace_namespaced_lease(self, name, namespace, body, **kwargs):
        self.kwargs.append(("replace", kwargs))
        if self.lease is None:
            raise not_found()
        if body.metadata.resource_version != self.lease.metadata.resource_version:
            raise ApiException(status=409, reason="Conflict")
        self._store("replace", body)
        return self.lease

    def delete_namespaced_lease(self, name, namespace, body, **kwargs):
        self.kwargs.append(("delete", kwargs))
        assert body.preconditions.resource_version == self.lease.metadata.resource_version
        self.writes.append(("delete", None))
        self.lease = None

    def _store(self, verb, body):
        self.version += 1
        self.lease = body.model_copy(deep=True)
        self.lease.metadata.resource_version = str(self.version)
        self.writes.append((verb, self.lease.spec.holder_identity))


def lease(holder, renewed: datetime):
    return client.V1Lease(
        metadata=client.V1ObjectMeta(name="suite-run", namespace="llm-journeys"),
        spec=client.V1LeaseSpec(holder_identity=holder, lease_duration_seconds=900, renew_time=renewed),
    )


NOW = datetime(2026, 11, 26, 12, 0, tzinfo=UTC)


def test_lease_held_fails_fast():
    api = FakeCoordination(lease("ci-runner/4242", NOW - timedelta(minutes=5)))
    with pytest.raises(tgt.LeaseHeld, match="ci-runner/4242"):
        with tgt.session_lease(make_target(), api=api, now=lambda: NOW, holder="me/1"):
            pytest.fail("the body ran while another run held the lease")
    assert api.writes == []
    assert api.lease.spec.holder_identity == "ci-runner/4242"


def test_expired_lease_taken_over():
    api = FakeCoordination(lease("crashed/99", NOW - timedelta(minutes=16)))
    with tgt.session_lease(make_target(), api=api, now=lambda: NOW, holder="me/1"):
        assert api.lease.spec.holder_identity == "me/1"
        assert api.lease.spec.lease_duration_seconds == 900
        assert api.lease.spec.renew_time == NOW
    assert api.writes == [("replace", "me/1"), ("delete", None)]
    assert api.lease is None


def test_lease_created_renewed_and_released():
    api = FakeCoordination()
    clock = iter([NOW, NOW + timedelta(seconds=60), NOW + timedelta(seconds=120)])
    with tgt.session_lease(make_target(), api=api, now=lambda: next(clock, NOW), holder="me/1", renew_every=0.05):
        deadline = time.monotonic() + 5
        while len(api.writes) < 2 and time.monotonic() < deadline:
            time.sleep(0.02)
        assert api.writes[0] == ("create", "me/1")
        assert api.writes[1] == ("replace", "me/1")
        assert api.lease.spec.renew_time > NOW
    assert api.writes[-1] == ("delete", None)


def test_lease_without_namespace_names_provisioning():
    class NoNamespace(FakeCoordination):
        def create_namespaced_lease(self, namespace, body, **kwargs):
            raise not_found()

    with pytest.raises(RuntimeError, match="namespace llm-journeys does not exist"):
        with tgt.session_lease(make_target(), api=NoNamespace(), now=lambda: NOW, holder="me/1"):
            pass


# --- fix round 1: lease renewal and release (I1, M2, M3) ---------------------


class Flaky(FakeCoordination):
    """Raises `error` from the first `failures` reads after the lease is taken."""

    def __init__(self, error, failures, lease=None):
        super().__init__(lease)
        self.error, self.failures = error, failures

    def read_namespaced_lease(self, name, namespace, **kwargs):
        if self.writes and self.failures:
            self.failures -= 1
            raise self.error
        return super().read_namespaced_lease(name, namespace, **kwargs)


def wait_until(condition, timeout=5.0):
    deadline = time.monotonic() + timeout
    while not condition() and time.monotonic() < deadline:
        time.sleep(0.02)
    assert condition()


def test_lease_renewer_survives_any_exception():
    from urllib3.exceptions import ProtocolError

    api = Flaky(ProtocolError("connection reset"), failures=2)
    with tgt.session_lease(make_target(), api=api, now=lambda: NOW, holder="me/1", renew_every=0.02):
        wait_until(lambda: ("replace", "me/1") in api.writes)
    assert api.writes[-1] == ("delete", None)


def test_lease_taken_over_fails_session():
    api = FakeCoordination()
    with pytest.raises(tgt.LeaseLost, match=r"llm-journeys/suite-run.*other/7"):
        with tgt.session_lease(make_target(), api=api, now=lambda: NOW, holder="me/1", renew_every=0.02) as held:
            api.lease.spec.holder_identity = "other/7"
            wait_until(lambda: held.lost is not None)
            with pytest.raises(tgt.LeaseLost, match="other/7"):
                held.check()
    assert api.lease.spec.holder_identity == "other/7"  # not released: it is not ours


def test_lease_deleted_fails_session():
    api = FakeCoordination()
    with pytest.raises(tgt.LeaseLost, match="deleted"):
        with tgt.session_lease(make_target(), api=api, now=lambda: NOW, holder="me/1", renew_every=0.02) as held:
            api.lease = None
            wait_until(lambda: held.lost is not None)


def test_lease_not_renewed_before_expiry_fails_session():
    api = Flaky(ApiException(status=500, reason="etcd down"), failures=10**6)
    clock = {"t": NOW}

    def now():
        clock["t"] += timedelta(minutes=4)
        return clock["t"]

    with pytest.raises(tgt.LeaseLost, match=r"not renewed.*900s"):
        with tgt.session_lease(make_target(), api=api, now=now, holder="me/1", renew_every=0.02) as held:
            wait_until(lambda: held.lost is not None)


def test_lease_vanished_during_conflict_retries():
    """M2: create loses a race (409), the winner's lease is gone on re-read: try once more."""

    class Race(FakeCoordination):
        raced = False

        def create_namespaced_lease(self, namespace, body, **kwargs):
            if not self.raced:
                self.raced = True
                raise ApiException(status=409, reason="AlreadyExists")
            return super().create_namespaced_lease(namespace, body, **kwargs)

    api = Race()
    with tgt.session_lease(make_target(), api=api, now=lambda: NOW, holder="me/1"):
        assert api.lease.spec.holder_identity == "me/1"


def test_lease_release_failure_does_not_mask_session_error(caplog):
    class NoDelete(FakeCoordination):
        def delete_namespaced_lease(self, name, namespace, body, **kwargs):
            raise ApiException(status=500, reason="boom")

    with pytest.raises(ValueError, match="the real failure"):
        with tgt.session_lease(make_target(), api=NoDelete(), now=lambda: NOW, holder="me/1"):
            raise ValueError("the real failure")
    assert "releasing session Lease llm-journeys/suite-run failed" in caplog.text
    with pytest.raises(ApiException):  # with no session error, a failed release is the error
        with tgt.session_lease(make_target(), api=NoDelete(), now=lambda: NOW, holder="me/1"):
            pass


def test_lease_calls_have_request_timeout():
    api = FakeCoordination()
    with tgt.session_lease(make_target(), api=api, now=lambda: NOW, holder="me/1"):
        pass
    assert api.kwargs and all(kw.get("_request_timeout") for _, kw in api.kwargs)


# --- fix round 1: port-forward supervisor and open_target (M4, M7) -----------


def test_port_forward_supervisor_survives_start_failure(fake_kubectl, tmp_path, monkeypatch):
    monkeypatch.setenv("FAKE_KUBECTL_EXIT_FIRST", str(tmp_path / "exited-once"))
    kubectl = tmp_path / "bin" / "kubectl"
    with tgt.port_forwards("kind-llm-serving-test", WANTED[:1], restart_delay=0.05) as forwards:
        kubectl.chmod(0o644)  # the restart cannot start kubectl (PermissionError)
        time.sleep(1.2)  # the first run exits after 0.5 s; restarts fail meanwhile
        kubectl.chmod(0o755)
        wait_until(lambda: len(calls(fake_kubectl)) >= 2, timeout=10)
        wait_until(lambda: answers(forwards[0].local_port), timeout=10)


def test_open_target_reads_record_once(tmp_path, monkeypatch):
    leaf = self_signed_leaf("llm.local")
    core = FakeCore(
        secrets={("llm-operator-system", "nebari-llm-shared-tls"): {"ca.crt": leaf}},
        configmaps={("llm-journeys", "provision"): record_data()},
        namespaces={"llm-operator-system", "llm-journeys"},
    )
    reads = []
    original = core.read_namespaced_config_map
    core.read_namespaced_config_map = lambda **kw: reads.append(kw) or original(**kw)
    cluster = fake_cluster(core)
    cluster.gateway_address = lambda: "127.0.0.1"
    monkeypatch.setattr(tgt.trust, "install_dns_mapping", lambda *a, **k: (lambda: None))
    monkeypatch.setattr(tgt, "_apps", lambda c: FakeApps({("keycloak", "keycloak"): {"KC_BOOTSTRAP_ADMIN_PASSWORD": "x"}}))

    @contextmanager
    def fake_forwards(context, wanted, **kwargs):
        yield [tgt.PortForward(ns, svc, port, 42000 + i) for i, (ns, svc, port) in enumerate(wanted)]

    monkeypatch.setattr(tgt, "port_forwards", fake_forwards)
    with tgt.open_target("kind-dev", "kind-llm-serving-test", cluster, tmp_path) as target:
        assert target.mock_vllm_image == "mock-vllm:dev"
    assert len(reads) == 1
