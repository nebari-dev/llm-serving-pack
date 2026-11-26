"""In-memory stand-ins for the kube APIs llm_journeys.target and preflight call,
and local HTTP servers for the HTTP verbs.

Only the methods those modules use exist; anything else raises AttributeError,
so a test notices when the code under test starts calling something new.
"""

import base64
import json
import ssl
import threading
from collections.abc import Callable
from contextlib import contextmanager
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import SimpleNamespace

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
from kubernetes import client
from kubernetes.client.rest import ApiException
from nebari_journeys.cluster import Cluster
from nebari_journeys.keycloak import Keycloak

from llm_journeys.target import Endpoint, ImageRecord, Target


def not_found():
    return ApiException(status=404, reason="Not Found")


class FakeCore:
    def __init__(self, *, secrets=None, configmaps=None, namespaces=(), pods=()):
        self.secrets = secrets or {}  # (namespace, name) -> {key: str}
        self.configmaps = configmaps or {}  # (namespace, name) -> {key: str}
        self.namespaces = set(namespaces)
        self.pods = list(pods)
        self.api_client = None

    def read_namespaced_secret(self, name, namespace):
        if (namespace, name) not in self.secrets:
            raise not_found()
        data = {k: base64.b64encode(v.encode()).decode() for k, v in self.secrets[(namespace, name)].items()}
        return client.V1Secret(data=data)

    def read_namespaced_config_map(self, name, namespace):
        if (namespace, name) not in self.configmaps:
            raise not_found()
        return client.V1ConfigMap(data=dict(self.configmaps[(namespace, name)]))

    def read_namespace(self, name):
        if name not in self.namespaces:
            raise not_found()
        return client.V1Namespace(metadata=client.V1ObjectMeta(name=name))

    def patch_namespaced_secret(self, name, namespace, body):
        if (namespace, name) not in self.secrets:
            raise not_found()
        self.secrets[(namespace, name)].update(body.get("stringData") or {})
        return client.V1Secret()

    def list_pod_for_all_namespaces(self, label_selector):
        wanted = dict(kv.split("=", 1) for kv in label_selector.split(","))
        items = [p for p in self.pods if wanted.items() <= (p.metadata.labels or {}).items()]
        return SimpleNamespace(items=items)


class FakeCustom:
    def __init__(self, objects=None):
        self.objects = objects or {}  # (group, namespace, plural) -> [dict]

    def list_namespaced_custom_object(self, group, version, namespace, plural):
        return {"items": list(self.objects.get((group, namespace, plural), []))}


class FakeApps:
    def __init__(self, env=None):
        self.env = env or {}  # (namespace, name) -> {container env name: value}
        self.reads = []

    def read_namespaced_deployment(self, name, namespace):
        self.reads.append((namespace, name))
        if (namespace, name) not in self.env:
            raise not_found()
        env = [client.V1EnvVar(name=k, value=v) for k, v in self.env[(namespace, name)].items()]
        return client.V1Deployment(
            spec=client.V1DeploymentSpec(
                selector=client.V1LabelSelector(),
                template=client.V1PodTemplateSpec(
                    spec=client.V1PodSpec(containers=[client.V1Container(name="main", env=env)])
                ),
            )
        )


class FakeObjects:
    """CustomObjectsApi for llm_journeys.models: objects keyed by (group, plural, namespace, name).

    `create_errors` are raised by create, one per call, before an object is
    stored. Delete removes the object at once.
    """

    def __init__(self):
        self.objects: dict[tuple[str, str, str, str], dict] = {}
        self.create_errors: list[Exception] = []
        self.creates = 0
        self.list_errors: dict[str, Exception] = {}  # plural -> raised by every list of it

    def add(self, group, plural, namespace, obj):
        obj.setdefault("metadata", {}).setdefault("namespace", namespace)
        self.objects[(group, plural, namespace, obj["metadata"]["name"])] = obj
        return obj

    def names(self, group, plural, namespace):
        return sorted(n for (g, p, ns, n) in self.objects if (g, p, ns) == (group, plural, namespace))

    def create_namespaced_custom_object(self, group, version, namespace, plural, body):
        self.creates += 1
        if self.create_errors:
            raise self.create_errors.pop(0)
        return self.add(group, plural, namespace, json.loads(json.dumps(body)))

    def get_namespaced_custom_object(self, group, version, namespace, plural, name):
        if (group, plural, namespace, name) not in self.objects:
            raise not_found()
        return self.objects[(group, plural, namespace, name)]

    def list_namespaced_custom_object(self, group, version, namespace, plural, label_selector=None):
        if plural in self.list_errors:
            raise self.list_errors[plural]
        def matches(obj):
            labels = obj.get("metadata", {}).get("labels") or {}
            for term in (label_selector or "").split(","):
                if not term:
                    continue
                key, sep, value = term.partition("=")
                if key not in labels or (sep and labels[key] != value):
                    return False
            return True

        items = [o for (g, p, ns, _), o in sorted(self.objects.items()) if (g, p, ns) == (group, plural, namespace)]
        return {"items": [o for o in items if matches(o)]}

    def delete_namespaced_custom_object(self, group, version, namespace, plural, name):
        if self.objects.pop((group, plural, namespace, name), None) is None:
            raise not_found()
        return {}


class FakeKeycloak:
    """The Keycloak admin calls the sweep and the scratch_user fixture make."""

    def __init__(self, users=()):
        self.by_id = {u["id"]: dict(u) for u in users}

    def users(self, search):
        return [u for u in self.by_id.values() if search in u["username"]]

    def delete_user(self, user_id):
        del self.by_id[user_id]

    def usernames(self):
        return sorted(u["username"] for u in self.by_id.values())


def record_data(*, mode="working-tree", nic_tag="", deviations=(), skipped=(), images=None):
    """ConfigMap data as dev/write-provision-record.sh writes it."""
    if images is None:
        images = {
            "app=mock-provider": {
                "reference": "mock-vllm:dev",
                "id": "sha256:" + "a" * 64,
                "repo_digests": ["docker.io/library/import-2026-11-26@sha256:" + "b" * 64],
            }
        }
    return {
        "mode": mode,
        "nic_tag": nic_tag,
        "deviations": json.dumps(list(deviations)),
        "skipped": json.dumps(list(skipped)),
        "images": json.dumps(images),
    }


def fake_cluster(core=None, custom=None):
    return Cluster(core=core or FakeCore(), custom=custom or FakeCustom())


def make_target(**overrides) -> Target:
    """A kind-dev Target with no cluster behind it; override any field."""
    fields = dict(
        name="kind-dev",
        cluster=fake_cluster(),
        namespace="llm-operator-system",
        external=Endpoint("https://llm.local:48443", True),
        internal=Endpoint("https://llm-internal.local:48443", True),
        key_manager=Endpoint("http://127.0.0.1:40001", True),
        kube_context="kind-llm-serving-test",
        ui=None,
        keycloak=Keycloak(base_url="http://127.0.0.1:40002", password="admin-secret"),
        issuer_url="http://keycloak.keycloak.svc.cluster.local:8080/realms/nebari",
        token_endpoint=Endpoint("http://127.0.0.1:40002", True),
        browser_pins=(),
        password_grant_client="nebari-frontend-spa",
        llm_group="llm-users",
        storage_class="standard",
        mock_vllm_image="mock-vllm:dev",
        mock_provider_model="mock-provider",
        operator_deployment="llm-operator",
        internal_gateway_name="nebari-gateway",
        operator_reconfig_allowed=True,
        expects_gateway_wide_policy=True,
        gateway_https_port=48443,
        usage_console=None,
        openmeter=Endpoint("http://127.0.0.1:40003", True),
        openmeter_cluster_url="http://usage-openmeter-api.usage-openmeter.svc",
        openmeter_client_label=("llm.nebari.dev/openmeter-client", "true"),
        usage_admin_group=None,
        nic_tag=None,
        deviations=(),
        provision_mode="working-tree",
        expected_images={"app=mock-provider": ImageRecord("mock-vllm:dev", "sha256:" + "a" * 64, ())},
        pack_installed=True,
    )
    fields.update(overrides)
    return Target(**fields)


def nebari_shaped(**overrides) -> Target:
    """A Target shaped like nebari: browser login, console, no reconfig, no policy."""
    fields = dict(
        name="nebari",
        ui=Endpoint("https://llm-keys.nebari.test", True),
        password_grant_client=None,
        operator_reconfig_allowed=False,
        expects_gateway_wide_policy=False,
        usage_console=Endpoint("https://llm-usage.nebari.test", True),
        usage_admin_group="llm-usage-admins",
        openmeter=None,
        openmeter_cluster_url=None,
        llm_group="llm",
    )
    fields.update(overrides)
    return make_target(**fields)


# --- local HTTP servers for the HTTP verbs (tokens, keys, gateway) ---------


@dataclass(frozen=True)
class Received:
    method: str
    path: str
    headers: tuple[tuple[str, str], ...]  # in the order sent; a repeated header is several entries
    body: bytes

    def header_values(self, name: str) -> list[str]:
        return [v for k, v in self.headers if k.lower() == name.lower()]


Reply = tuple[int, object]  # (status, body); a dict or list body is sent as JSON


@contextmanager
def http_fake(respond: Callable[[Received], Reply], *, tls: tuple[Path, Path] | None = None):
    """A local HTTP/1.1 server; yields (base_url, received requests).

    With tls=(cert, key) it serves HTTPS as https://localhost:<port>.
    """
    received: list[Received] = []

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def _any(self):
            length = int(self.headers.get("Content-Length") or 0)
            request = Received(self.command, self.path, tuple(self.headers.items()), self.rfile.read(length))
            received.append(request)
            status, body = respond(request)
            data = json.dumps(body).encode() if isinstance(body, (dict, list)) else str(body or "").encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json" if isinstance(body, (dict, list)) else "text/plain")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        do_GET = do_POST = do_DELETE = _any

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    scheme, host = "http", "127.0.0.1"
    if tls is not None:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(*tls)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        scheme, host = "https", "localhost"
    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True)
    thread.start()
    try:
        yield f"{scheme}://{host}:{server.server_address[1]}", received
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def self_signed_cert(directory: Path, hostname: str = "localhost") -> tuple[Path, Path]:
    """A self-signed leaf for hostname, written as (cert.pem, key.pem); the cert is its own CA."""
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
    cert_path, key_path = directory / "cert.pem", directory / "key.pem"
    cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(
        key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
    )
    return cert_path, key_path
