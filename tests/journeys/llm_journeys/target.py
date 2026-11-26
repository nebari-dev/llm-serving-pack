"""The suite-to-cluster seam: which cluster, which endpoints, which trust.

A Target is the only thing that differs between the kind-dev and nebari
profiles. Tests and verbs read it and never look at the environment.

Kube context safety (Q47): every kube call and every kubectl names the
context given with --kube-context. The context must be a kind context
(name `kind-...`) whose API server is on a loopback address and not behind
a proxy-url, which keeps the suite off a real cluster by mistake; it is a
guard against a wrong context, not against a kubeconfig made to look local
(a port-forward or tunnel to a remote API server passes it). The kubeconfig
file is never written, so the current context never changes.
"""

import ipaddress
import json
import logging
import os
import socket
import subprocess
import tempfile
import threading
import time
from collections.abc import Iterator, Mapping, Sequence
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Literal
from urllib.parse import urlsplit

from kubernetes import client
from kubernetes import config as kubeconfig
from kubernetes.client.rest import ApiException
from nebari_journeys import trust
from nebari_journeys.cluster import Cluster
from nebari_journeys.keycloak import Keycloak

log = logging.getLogger(__name__)

TargetName = Literal["kind-dev", "nebari"]

NOT_FOUND = 404
CONFLICT = 409

# Where the gateway is published on the host on both targets (spec, "Reaching
# the cluster"): the kind node's host ports listen on 127.0.0.1.
GATEWAY_HOST_ADDRESS = "127.0.0.1"
DEFAULT_KIND_DEV_HTTPS_PORT = 48443

# The provisioning record and the session lease live in a namespace that
# provisioning creates and owns (Q84).
JOURNEYS_NAMESPACE = "llm-journeys"
RECORD_NAME = "provision"
LEASE_NAME = "suite-run"
LEASE_DURATION_SECONDS = 15 * 60
LEASE_RENEW_SECONDS = 60.0
LEASE_REQUEST_TIMEOUT = 10  # seconds, every Lease API call

# The realm both Keycloaks serve the pack from; a password grant goes to
# Target.token_endpoint.base_url + REALM_TOKEN_PATH.
REALM = "nebari"
REALM_TOKEN_PATH = f"/realms/{REALM}/protocol/openid-connect/token"

# The operator's shared listener hostnames and their TLS secret
# (operator/internal/controller/reconcilers/common.go SharedExternalHostname,
# SharedInternalHostname; clustertls.go SharedTLSSecretName).
EXTERNAL_HOST_LABEL = "llm"
INTERNAL_HOST_LABEL = "llm-internal"
SHARED_TLS_SECRET = "nebari-llm-shared-tls"

JOURNEY_LABEL = "nebari.dev/test-journey"

# kind-dev profile values (docs/architecture/contracts.md "`kind-dev` cluster
# profile"; dev/manifests/operator.yaml, dev/integration-up.sh).
KIND_DEV_DOMAIN = "local"  # LLM_BASE_DOMAIN in dev/manifests/operator.yaml
KIND_DEV_PACK_NAMESPACE = "llm-operator-system"
KIND_DEV_KEY_MANAGER = ("llm-operator-system", "llm-key-manager", 8080)
KIND_DEV_KEYCLOAK = ("keycloak", "keycloak", 8080)
KIND_DEV_OPENMETER = ("usage-openmeter", "usage-openmeter-api", 80)
KIND_DEV_KEYCLOAK_DEPLOYMENT = ("keycloak", "keycloak")
KIND_DEV_KEYCLOAK_ADMIN_ENV = "KC_BOOTSTRAP_ADMIN_PASSWORD"
KIND_DEV_ISSUER = "http://keycloak.keycloak.svc.cluster.local:8080/realms/nebari"
KIND_DEV_PASSWORD_GRANT_CLIENT = "nebari-frontend-spa"
KIND_DEV_STORAGE_CLASS = "standard"  # kind's default StorageClass
MOCK_PROVIDER_SELECTOR = "app=mock-provider"
OPENMETER_SKIP = "openmeter"
OPENMETER_CLIENT_LABEL = ("llm.nebari.dev/openmeter-client", "true")


class UnsafeContext(RuntimeError):
    """The kube context is not a local kind cluster; the suite will not use it."""


class LeaseHeld(RuntimeError):
    """Another suite run holds the session lease on this cluster."""


class LeaseLost(RuntimeError):
    """This run lost the session lease: taken over, deleted, or not renewed in time."""


@dataclass(frozen=True)
class Endpoint:
    base_url: str  # scheme://host[:port], no trailing slash; host is the real hostname
    verify: str | bool  # CA bundle path, or True for the system store; never False

    def __post_init__(self):
        if self.verify is False:
            raise ValueError(f"Endpoint {self.base_url}: verify is never False")
        if self.base_url.endswith("/"):
            raise ValueError(f"Endpoint {self.base_url}: base_url has a trailing slash")


@dataclass(frozen=True)
class ImageRecord:  # one per commit-built image, as the kind node holds it after `kind load` (Q89)
    reference: str  # repository:tag as built
    image_id: str  # containerd's image id on the node; never Docker's image ID
    repo_digests: tuple[str, ...]  # the sha256:<hex> digest of every repoDigests entry on the node


@dataclass(frozen=True)
class PortForward:  # kind-dev only, for services not behind a gateway (Q58)
    namespace: str
    service: str
    remote_port: int
    local_port: int  # chosen free port; kept across a restart of the forward


@dataclass(frozen=True)
class Target:
    name: TargetName
    cluster: Cluster
    namespace: str
    external: Endpoint
    internal: Endpoint
    key_manager: Endpoint
    kube_context: str
    ui: Endpoint | None
    keycloak: Keycloak
    issuer_url: str
    token_endpoint: Endpoint
    browser_pins: tuple[str, ...]
    password_grant_client: str | None
    llm_group: str
    storage_class: str
    mock_vllm_image: str
    mock_provider_model: str
    operator_deployment: str
    internal_gateway_name: str
    operator_reconfig_allowed: bool
    expects_gateway_wide_policy: bool
    gateway_https_port: int
    usage_console: Endpoint | None
    openmeter: Endpoint | None
    openmeter_cluster_url: str | None
    openmeter_client_label: tuple[str, str]
    usage_admin_group: str | None
    nic_tag: str | None
    deviations: tuple[str, ...]
    provision_mode: str
    expected_images: Mapping[str, ImageRecord]
    pack_installed: bool

    def __post_init__(self):
        if (self.ui is None) == (self.password_grant_client is None):
            raise ValueError(
                "Target: exactly one of ui and password_grant_client is set "
                "(tokens come from a browser login or from a password grant)"
            )
        if (self.usage_console is None) != (self.usage_admin_group is None):
            raise ValueError("Target: usage_console and usage_admin_group are both set or both None")
        if self.usage_console is not None and self.openmeter is not None:
            raise ValueError("Target: at most one of usage_console and openmeter is set")
        if self.operator_reconfig_allowed and self.name != "kind-dev":
            raise ValueError("Target: operator_reconfig_allowed is true only on kind-dev")


def _kubeconfig_file() -> str:
    """The kubeconfig path, read at call time (the client reads $KUBECONFIG at import)."""
    return os.environ.get("KUBECONFIG", "~/.kube/config")


def _is_loopback(host: str | None) -> bool:
    if not host:
        return False
    if host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def require_local_kind_context(kube_context: str) -> None:
    """Refuse a context that is not kind-* with a loopback API server and no proxy-url (Q47).

    Reads the kubeconfig into a fresh Configuration: nothing is written to the
    file and the process-wide default is left alone.
    """
    if not kube_context.startswith("kind-"):
        raise UnsafeContext(
            f"refusing kube context {kube_context!r}: the suite runs only on a local kind "
            "cluster (a context named kind-...)"
        )
    probe = client.Configuration()
    try:
        kubeconfig.load_kube_config(
            config_file=_kubeconfig_file(),
            context=kube_context,
            client_configuration=probe,
            persist_config=False,
        )
    except kubeconfig.ConfigException as error:
        raise UnsafeContext(f"refusing kube context {kube_context!r}: {error}") from error
    host = urlsplit(probe.host if "://" in (probe.host or "") else f"https://{probe.host}").hostname
    if not _is_loopback(host):
        raise UnsafeContext(
            f"refusing kube context {kube_context!r}: its API server {probe.host} is not on a "
            "loopback address, so it is not a local kind cluster"
        )
    if probe.proxy:
        # Through a proxy, a loopback address names whatever the proxy reaches.
        raise UnsafeContext(
            f"refusing kube context {kube_context!r}: it reaches its API server {probe.host} through "
            f"proxy {probe.proxy}, so it may not be a local kind cluster"
        )


@contextmanager
def connect_cluster(kube_context: str) -> Iterator[Cluster]:
    """A Cluster on the named context; never the current one (Q47, R5-F3).

    load_kube_config(context=...) sets the in-process default configuration,
    which Cluster.api_host() reads, and never writes the kubeconfig file.
    """
    require_local_kind_context(kube_context)
    kubeconfig.load_kube_config(
        config_file=_kubeconfig_file(),
        context=kube_context,
        persist_config=False,
    )
    api = client.ApiClient()
    try:
        yield Cluster(
            core=client.CoreV1Api(api),
            custom=client.CustomObjectsApi(api),
            storage=client.StorageV1Api(api),
        )
    finally:
        api.close()


def _apps(cluster: Cluster) -> client.AppsV1Api:
    return client.AppsV1Api(cluster.core.api_client)


def _coordination(cluster: Cluster) -> client.CoordinationV1Api:
    return client.CoordinationV1Api(cluster.core.api_client)


def deployment_env(apps, namespace: str, name: str, container_env: str) -> str | None:
    """A container env value of a Deployment, or None when no container sets it."""
    deployment = apps.read_namespaced_deployment(name=name, namespace=namespace)
    for container in deployment.spec.template.spec.containers or []:
        for env in container.env or []:
            if env.name == container_env:
                return env.value
    return None


def _namespace_exists(cluster: Cluster, name: str) -> bool:
    try:
        cluster.core.read_namespace(name=name)
    except ApiException as error:
        if error.status == NOT_FOUND:
            return False
        raise
    return True


def endpoint_from_secret(
    cluster: Cluster, base_url: str, tls_secret: tuple[str, str], workdir: Path
) -> Endpoint:
    """An Endpoint verified against the CA in its own TLS secret (Q44).

    `tls_secret` is (namespace, name). ca.crt is preferred; a self-signed leaf
    with no ca.crt is its own anchor, so tls.crt is the fallback, as in
    nebari_journeys.trust.trust_anchor_pem. A missing secret raises: there is
    no silent fallback to the system store.
    """
    namespace, name = tls_secret
    pem = None
    for key in ("ca.crt", "tls.crt"):
        try:
            value = cluster.secret_value(namespace, name, key)
        except KeyError:
            continue
        except ApiException as error:
            raise RuntimeError(
                f"cannot read TLS secret {namespace}/{name} for {base_url}: {error.status} {error.reason}"
            ) from error
        if value.strip():
            pem = value
            break
    if pem is None:
        raise RuntimeError(f"TLS secret {namespace}/{name} for {base_url} has neither ca.crt nor tls.crt")
    path = Path(workdir) / f"ca-{namespace}-{name}.pem"
    path.write_text(pem)
    return Endpoint(base_url=base_url, verify=str(path))


def _domain(target: Target) -> str:
    """The platform domain: the external host is llm.<domain> on both targets."""
    host = urlsplit(target.external.base_url).hostname or ""
    return host.split(".", 1)[1] if "." in host else host


def chromium_args(target: Target) -> list[str]:
    """Chromium flags for the target: the name mapping and one SPKI flag with every pin (Q44).

    nebari_journeys.trust.chromium_args pins one anchor; it is called without
    one for the resolver rules, and the pins go in a single comma-separated
    --ignore-certificate-errors-spki-list, each hash once.
    """
    args = trust.chromium_args(_domain(target), GATEWAY_HOST_ADDRESS, True)
    hashes = list(dict.fromkeys(trust.spki_sha256_b64(pem) for pem in target.browser_pins))
    if hashes:
        args.append(f"--ignore-certificate-errors-spki-list={','.join(hashes)}")
    return args


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind((GATEWAY_HOST_ADDRESS, 0))
        return sock.getsockname()[1]


def _port_answers(port: int, *, host: str = GATEWAY_HOST_ADDRESS, timeout: float = 1.0) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


class _Forward:
    """One kubectl port-forward, restarted on the same local port when it exits."""

    def __init__(self, context: str, forward: PortForward, logdir: str):
        self.forward = forward
        self.argv = [
            "kubectl",
            "--context",
            context,
            "-n",
            forward.namespace,
            "port-forward",
            "--address",
            GATEWAY_HOST_ADDRESS,
            f"svc/{forward.service}",
            f"{forward.local_port}:{forward.remote_port}",
        ]
        self.logpath = Path(logdir) / f"{forward.namespace}-{forward.service}.log"
        self.proc: subprocess.Popen | None = None
        self.started_at = 0.0
        self.delay = 0.0  # restart backoff, set by port_forwards

    def start(self) -> None:
        self.started_at = time.monotonic()
        with self.logpath.open("ab") as out:
            self.proc = subprocess.Popen(self.argv, stdout=out, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL)

    def exited(self) -> bool:
        return self.proc is not None and self.proc.poll() is not None

    def stop(self) -> None:
        if self.proc is None or self.proc.poll() is not None:
            return
        self.proc.terminate()
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()

    def log_tail(self) -> str:
        try:
            return " | ".join(self.logpath.read_text(errors="replace").strip().splitlines()[-3:])
        except OSError:
            return ""


@contextmanager
def port_forwards(
    context: str,
    wanted: Sequence[tuple[str, str, int]],
    *,
    ready_timeout: float = 30.0,
    restart_delay: float = 1.0,
    restart_delay_cap: float = 30.0,
) -> Iterator[list[PortForward]]:
    """kubectl port-forwards pinned to `context`, one per (namespace, service, port).

    A forward that exits is restarted on the same local port, after a delay
    that doubles up to `restart_delay_cap` while it keeps exiting quickly. A
    restart that cannot start kubectl is logged and retried. Every forward is
    stopped when the block ends, also when it raises.
    """
    stopping = threading.Event()
    with ExitStack() as stack:
        logdir = stack.enter_context(tempfile.TemporaryDirectory(prefix="llm-journeys-pf-"))
        forwards = [_Forward(context, PortForward(ns, svc, port, _free_port()), logdir) for ns, svc, port in wanted]
        stack.callback(lambda: [fwd.stop() for fwd in forwards])

        for fwd in forwards:
            fwd.start()

        for fwd in forwards:
            fwd.delay = restart_delay

        def supervise():
            while not stopping.wait(0.1):
                for fwd in forwards:
                    if stopping.is_set():
                        return
                    if not fwd.exited():
                        continue
                    if time.monotonic() - fwd.started_at > restart_delay_cap:
                        fwd.delay = restart_delay  # it ran a while: not a crash loop
                    log.warning(
                        "port-forward %s exited; restarting in %gs: %s", fwd.argv, fwd.delay, fwd.log_tail()
                    )
                    if stopping.wait(fwd.delay):
                        return
                    fwd.delay = min(fwd.delay * 2, restart_delay_cap)
                    try:
                        fwd.start()
                    except Exception as error:  # noqa: BLE001 - keep supervising; retried on the next pass
                        log.warning("starting port-forward %s failed: %s", fwd.argv, error)

        supervisor = threading.Thread(target=supervise, name="port-forwards", daemon=True)
        supervisor.start()
        # Exit runs callbacks last-in first-out: stop the supervisor, wait for
        # it (so it cannot restart a forward), then stop every forward.
        stack.callback(supervisor.join)
        stack.callback(stopping.set)

        deadline = time.monotonic() + ready_timeout
        for fwd in forwards:
            while not _port_answers(fwd.forward.local_port):
                if time.monotonic() > deadline:
                    raise RuntimeError(
                        f"port-forward to svc/{fwd.forward.service} in {fwd.forward.namespace} "
                        f"(context {context}) not answering on {GATEWAY_HOST_ADDRESS}:{fwd.forward.local_port} "
                        f"after {ready_timeout:g}s: {fwd.log_tail()}"
                    )
                time.sleep(0.1)
        yield [fwd.forward for fwd in forwards]


def _json_field(data: Mapping[str, str], key: str, kind: type):
    if key not in data:
        raise ValueError(f"provisioning record {JOURNEYS_NAMESPACE}/{RECORD_NAME} has no key {key!r}")
    value = data[key] if kind is str else json.loads(data[key])
    if not isinstance(value, kind):
        raise ValueError(
            f"provisioning record {JOURNEYS_NAMESPACE}/{RECORD_NAME} key {key!r}: "
            f"want {kind.__name__}, got {type(value).__name__}"
        )
    return value


def _digest(repo_digest: str) -> str:
    return repo_digest.split("@", 1)[1] if "@" in repo_digest else repo_digest


def read_provision_record(cluster: Cluster) -> dict:
    """The provisioning record, parsed; {} when the ConfigMap does not exist (Q84).

    Keys: mode, nic_tag, deviations, skipped, images (selector -> ImageRecord).
    A missing record is a preflight problem, not an error here.
    """
    try:
        configmap = cluster.core.read_namespaced_config_map(name=RECORD_NAME, namespace=JOURNEYS_NAMESPACE)
    except ApiException as error:
        if error.status == NOT_FOUND:
            return {}
        raise
    data = configmap.data or {}
    images = {
        selector: ImageRecord(
            reference=image["reference"],
            image_id=image["id"],
            repo_digests=tuple(_digest(d) for d in image["repo_digests"]),
        )
        for selector, image in _json_field(data, "images", dict).items()
    }
    return {
        "mode": _json_field(data, "mode", str),
        "nic_tag": _json_field(data, "nic_tag", str),
        "deviations": tuple(_json_field(data, "deviations", list)),
        "skipped": tuple(_json_field(data, "skipped", list)),
        "images": images,
    }


def pod_images(target: Target) -> dict[str, tuple[str, str]]:
    """Selector -> (image reference, imageID) of a Running pod it matches (Q78, Q81).

    Test-owned pods are left out. The container is the one whose spec image is
    the recorded reference, in full or after a registry path ("/" + reference);
    the status image is not used (it can be a bare sha256). A Running pod with
    no such container raises, naming the selector. A selector with no Running
    pod is absent from the result.
    """
    running = {}
    for selector, record in target.expected_images.items():
        pods = target.cluster.core.list_pod_for_all_namespaces(label_selector=selector).items
        pods = sorted(
            (
                p
                for p in pods
                if p.status.phase == "Running" and (p.metadata.labels or {}).get(JOURNEY_LABEL) != "true"
            ),
            key=lambda p: (p.metadata.namespace, p.metadata.name),
        )
        for pod in pods:
            spec_images = {c.name: c.image for c in pod.spec.containers}
            matching = {
                name
                for name, image in spec_images.items()
                if image == record.reference or image.endswith("/" + record.reference)
            }
            if not matching:
                raise RuntimeError(
                    f"pod {pod.metadata.namespace}/{pod.metadata.name} matched by {selector} runs no container "
                    f"with image {record.reference} (containers: {sorted(spec_images.values())})"
                )
            statuses = [s for s in pod.status.container_statuses or [] if s.name in matching and s.image_id]
            if statuses:
                running[selector] = (spec_images[statuses[0].name], statuses[0].image_id)
                break
    return running


def kind_dev_target(
    kube_context: str,
    cluster: Cluster,
    workdir: Path,
    forwards: Sequence[PortForward],
    *,
    gateway_https_port: int = DEFAULT_KIND_DEV_HTTPS_PORT,
    record: dict | None = None,
) -> Target:
    """The kind-dev Target: gateway on 127.0.0.1, the rest through `forwards`.

    `record` is the provisioning record when the caller has read it already.
    The gateway endpoints are verified against the shared TLS secret's CA,
    except when the pack is not installed (see gateway_endpoint below).
    """
    by_service = {(f.namespace, f.service): f for f in forwards}
    key_manager = by_service[KIND_DEV_KEY_MANAGER[:2]]
    keycloak = by_service[KIND_DEV_KEYCLOAK[:2]]
    openmeter = by_service.get(KIND_DEV_OPENMETER[:2])
    if record is None:
        record = read_provision_record(cluster)
    images = record.get("images", {})
    pack_installed = _namespace_exists(cluster, KIND_DEV_PACK_NAMESPACE)

    def gateway_endpoint(label: str) -> Endpoint:
        url = f"https://{label}.{KIND_DEV_DOMAIN}:{gateway_https_port}"
        if not pack_installed:
            # The one deliberate exception to per-endpoint trust (Q44): with
            # no pack there is no shared TLS secret, so the endpoint falls
            # back to the system store (still verified, never verify=False).
            # Nothing behind it can pass: the tests that need the pack fail
            # on their own, and preflight notes "pack not installed" (Q84).
            return Endpoint(url, True)
        return endpoint_from_secret(cluster, url, (KIND_DEV_PACK_NAMESPACE, SHARED_TLS_SECRET), workdir)

    external = gateway_endpoint(EXTERNAL_HOST_LABEL)
    internal = gateway_endpoint(INTERNAL_HOST_LABEL)
    pins = tuple(
        dict.fromkeys(Path(e.verify).read_text() for e in (external, internal) if isinstance(e.verify, str))
    )
    keycloak_url = f"http://{GATEWAY_HOST_ADDRESS}:{keycloak.local_port}"
    admin_password = deployment_env(_apps(cluster), *KIND_DEV_KEYCLOAK_DEPLOYMENT, KIND_DEV_KEYCLOAK_ADMIN_ENV)
    if not admin_password:
        raise RuntimeError(
            f"Deployment keycloak/keycloak sets no {KIND_DEV_KEYCLOAK_ADMIN_ENV}; run make -C dev integration-up"
        )
    mock = images.get(MOCK_PROVIDER_SELECTOR)
    return Target(
        name="kind-dev",
        cluster=cluster,
        namespace=KIND_DEV_PACK_NAMESPACE,
        external=external,
        internal=internal,
        key_manager=Endpoint(f"http://{GATEWAY_HOST_ADDRESS}:{key_manager.local_port}", True),
        kube_context=kube_context,
        ui=None,
        keycloak=Keycloak(base_url=keycloak_url, password=admin_password),
        issuer_url=KIND_DEV_ISSUER,
        token_endpoint=Endpoint(keycloak_url, True),
        browser_pins=pins,
        password_grant_client=KIND_DEV_PASSWORD_GRANT_CLIENT,
        llm_group="llm-users",
        storage_class=KIND_DEV_STORAGE_CLASS,
        mock_vllm_image=mock.reference if mock else "",
        mock_provider_model="mock-provider",
        operator_deployment="llm-operator",
        internal_gateway_name="nebari-gateway",
        operator_reconfig_allowed=True,
        expects_gateway_wide_policy=True,
        gateway_https_port=gateway_https_port,
        usage_console=None,
        openmeter=Endpoint(f"http://{GATEWAY_HOST_ADDRESS}:{openmeter.local_port}", True) if openmeter else None,
        openmeter_cluster_url=f"http://{KIND_DEV_OPENMETER[1]}.{KIND_DEV_OPENMETER[0]}.svc" if openmeter else None,
        openmeter_client_label=OPENMETER_CLIENT_LABEL,
        usage_admin_group=None,
        nic_tag=record.get("nic_tag") or None,
        deviations=record.get("deviations", ()),
        provision_mode=record.get("mode", ""),
        expected_images=images,
        pack_installed=pack_installed,
    )


def nebari_target(kube_context: str, cluster: Cluster, workdir: Path) -> Target:
    raise NotImplementedError("Slice 12")


@contextmanager
def open_target(
    name: TargetName,
    kube_context: str,
    cluster: Cluster,
    workdir: Path,
    *,
    gateway_https_port: int | None = None,
) -> Iterator[Target]:
    """The Target for `name`; on kind-dev it owns the port-forwards and the name mapping.

    Opening a target never changes the cluster.
    """
    if name == "nebari":
        yield nebari_target(kube_context, cluster, workdir)
        return
    if name != "kind-dev":
        raise ValueError(f"unknown target {name!r}")
    port = gateway_https_port or DEFAULT_KIND_DEV_HTTPS_PORT
    address = cluster.gateway_address()
    undo = trust.install_dns_mapping(KIND_DEV_DOMAIN, address, exempt_hosts=[cluster.api_host() or ""])
    try:
        record = read_provision_record(cluster)
        wanted = [KIND_DEV_KEY_MANAGER, KIND_DEV_KEYCLOAK]
        if OPENMETER_SKIP not in record.get("skipped", ()):
            wanted.append(KIND_DEV_OPENMETER)
        with port_forwards(kube_context, wanted) as forwards:
            yield kind_dev_target(
                kube_context, cluster, workdir, forwards, gateway_https_port=port, record=record
            )
    finally:
        undo()


def _holder() -> str:
    return f"{socket.gethostname()}/{os.getpid()}"


def _now() -> datetime:
    return datetime.now(UTC)


def _lease_body(holder: str, now: datetime, resource_version: str | None = None) -> client.V1Lease:
    return client.V1Lease(
        metadata=client.V1ObjectMeta(name=LEASE_NAME, namespace=JOURNEYS_NAMESPACE, resource_version=resource_version),
        spec=client.V1LeaseSpec(
            holder_identity=holder,
            lease_duration_seconds=LEASE_DURATION_SECONDS,
            acquire_time=now,
            renew_time=now,
        ),
    )


def _held_by_other(lease: client.V1Lease, holder: str, now: datetime) -> bool:
    spec = lease.spec
    if spec is None or not spec.holder_identity or spec.holder_identity == holder:
        return False
    last = spec.renew_time or spec.acquire_time
    if last is None:
        return False
    return last + timedelta(seconds=spec.lease_duration_seconds or LEASE_DURATION_SECONDS) > now


def _held_message(lease: client.V1Lease) -> str:
    spec = lease.spec
    return (
        f"another suite run holds Lease {JOURNEYS_NAMESPACE}/{LEASE_NAME} on this cluster: "
        f"holder {spec.holder_identity}, last renewed {spec.renew_time or spec.acquire_time}, "
        f"expires after {spec.lease_duration_seconds or LEASE_DURATION_SECONDS}s without renewal"
    )


LEASE = f"Lease {JOURNEYS_NAMESPACE}/{LEASE_NAME}"
_CALL = {"name": LEASE_NAME, "namespace": JOURNEYS_NAMESPACE, "_request_timeout": LEASE_REQUEST_TIMEOUT}


def _acquire(api, holder: str, now: datetime, *, retry: bool = True) -> None:
    try:
        current = api.read_namespaced_lease(**_CALL)
    except ApiException as error:
        if error.status != NOT_FOUND:
            raise
        current = None
    try:
        if current is None:
            api.create_namespaced_lease(
                namespace=JOURNEYS_NAMESPACE, body=_lease_body(holder, now), _request_timeout=LEASE_REQUEST_TIMEOUT
            )
            return
        if _held_by_other(current, holder, now):
            raise LeaseHeld(_held_message(current))
        api.replace_namespaced_lease(body=_lease_body(holder, now, current.metadata.resource_version), **_CALL)
    except ApiException as error:
        if error.status == CONFLICT:
            # Another run created or took the lease between our read and write.
            try:
                winner = api.read_namespaced_lease(**_CALL)
            except ApiException as again:
                if again.status != NOT_FOUND:
                    raise
                # ...and it is gone again: try once more from the start.
                if retry:
                    return _acquire(api, holder, now, retry=False)
                raise LeaseHeld(
                    f"{LEASE} changed hands twice while this run tried to take it; another run is starting "
                    "or ending on this cluster"
                ) from error
            raise LeaseHeld(_held_message(winner)) from error
        if error.status == NOT_FOUND:
            raise RuntimeError(
                f"namespace {JOURNEYS_NAMESPACE} does not exist, so the session lease cannot be taken; "
                "the profile is not provisioned (make -C dev integration-up)"
            ) from error
        raise


def _renew(api, holder: str, now: datetime) -> str | None:
    """Renew the lease; the reason it is lost, or None when it was renewed."""
    try:
        lease = api.read_namespaced_lease(**_CALL)
    except ApiException as error:
        if error.status == NOT_FOUND:
            return f"{LEASE} was deleted while this run ({holder}) held it"
        raise
    if lease.spec is None or lease.spec.holder_identity != holder:
        other = lease.spec.holder_identity if lease.spec else None
        return f"{LEASE} was taken over by {other} while this run ({holder}) held it"
    lease.spec.renew_time = now
    api.replace_namespaced_lease(body=lease, **_CALL)
    return None


def _release(api, holder: str) -> None:
    try:
        lease = api.read_namespaced_lease(**_CALL)
    except ApiException as error:
        if error.status == NOT_FOUND:
            return
        raise
    if lease.spec is None or lease.spec.holder_identity != holder:
        return
    api.delete_namespaced_lease(
        body=client.V1DeleteOptions(
            preconditions=client.V1Preconditions(resource_version=lease.metadata.resource_version)
        ),
        **_CALL,
    )


def _describe(error: BaseException) -> str:
    """A short, secret-free description of a kube call failure."""
    if isinstance(error, ApiException):
        return f"ApiException {error.status} {error.reason}"
    return f"{type(error).__name__}: {error}"


class SessionLease:
    """What session_lease yields: `lost` is the reason this run lost the lease, or None."""

    def __init__(self):
        self.lost: str | None = None

    def check(self) -> None:
        """Raise LeaseLost now if the lease is lost."""
        if self.lost is not None:
            raise LeaseLost(self.lost)


@contextmanager
def session_lease(
    target: Target,
    *,
    api=None,
    now=_now,
    holder: str | None = None,
    renew_every: float = LEASE_RENEW_SECONDS,
) -> Iterator[SessionLease]:
    """Hold Lease llm-journeys/suite-run for the session (R6-F9).

    A lease another run holds and renewed within its 15 minute duration fails
    fast, naming the holder; an expired one is taken over. Renewed every 60 s;
    a failed renewal is logged and retried on the next tick. If the lease is
    taken over, deleted, or not renewed before its own expiry, the session
    fails: LeaseLost is raised from `check()` and when the block ends. It is
    released at the end; a failed release does not mask an error the block
    already raised.
    """
    api = api if api is not None else _coordination(target.cluster)
    holder = holder or _holder()
    acquired_at = now()
    _acquire(api, holder, acquired_at)
    held = SessionLease()
    stop = threading.Event()

    def renew_loop():
        last = acquired_at
        while not stop.wait(renew_every):
            tick = now()
            try:
                reason = _renew(api, holder, tick)
            except Exception as error:  # noqa: BLE001 - any failure is retried until the lease expires
                log.warning("renewing session %s failed, retrying: %s", LEASE, _describe(error))
                if tick - last < timedelta(seconds=LEASE_DURATION_SECONDS):
                    continue
                reason = (
                    f"{LEASE} was not renewed since {last}, past its {LEASE_DURATION_SECONDS}s duration, "
                    f"so another run may have taken it (last error: {_describe(error)})"
                )
            if reason is None:
                last = tick
                continue
            held.lost = reason
            log.error("session lease lost: %s", reason)
            return

    renewer = threading.Thread(target=renew_loop, name="session-lease", daemon=True)
    renewer.start()
    failed = False
    try:
        yield held
    except BaseException:
        failed = True
        raise
    finally:
        stop.set()
        renewer.join(timeout=3 * LEASE_REQUEST_TIMEOUT)
        if held.lost is None:
            try:
                _release(api, holder)
            except Exception as error:
                if not failed:
                    raise
                log.error("releasing session %s failed: %s", LEASE, _describe(error))
        if held.lost is not None:
            if not failed:
                raise LeaseLost(held.lost)
            log.error("session lease lost: %s", held.lost)
