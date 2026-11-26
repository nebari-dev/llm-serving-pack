"""llm_journeys.gateway: what chat puts on the wire, and where retries happen.

No cluster: local servers. http_fake speaks HTTP/1.1; h2_fake below speaks
HTTP/2 when the client offers it (ALPN) and HTTP/1.1 otherwise.
"""

import json
import socket
import ssl
import threading
import time
from contextlib import contextmanager

import h2.config
import h2.connection
import h2.events
import httpx
import pytest
import requests

from llm_journeys import gateway
from llm_journeys.target import Endpoint
from tests_lib.fakes import http_fake, self_signed_cert

CHAT_PATH = "/v1/chat/completions"


def ok(request):
    return 200, {"choices": []}


@contextmanager
def h2_fake(cert, key):
    """A TLS server that records the protocol ALPN chose for each connection and answers 200."""
    protocols: list[str] = []
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)
    context.set_alpn_protocols(["h2", "http/1.1"])
    listener = socket.create_server(("127.0.0.1", 0))
    listener.settimeout(0.2)
    stop = threading.Event()

    def answer_h2(conn):
        h2conn = h2.connection.H2Connection(h2.config.H2Configuration(client_side=False))
        h2conn.initiate_connection()
        conn.sendall(h2conn.data_to_send())
        while data := conn.recv(65535):
            for event in h2conn.receive_data(data):
                if isinstance(event, h2.events.DataReceived):
                    h2conn.acknowledge_received_data(event.flow_controlled_length, event.stream_id)
                elif isinstance(event, h2.events.StreamEnded):
                    h2conn.send_headers(event.stream_id, [(":status", "200"), ("content-length", "0")], end_stream=True)
            conn.sendall(h2conn.data_to_send())

    def answer_h1(conn):
        data = b""
        while b"\r\n\r\n" not in data:
            data += conn.recv(65535)
        head, _, body = data.partition(b"\r\n\r\n")
        length = next(
            int(line.split(b":", 1)[1]) for line in head.split(b"\r\n") if line.lower().startswith(b"content-length:")
        )
        while len(body) < length:
            body += conn.recv(65535)
        conn.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")

    def serve():
        while not stop.is_set():
            try:
                raw, _ = listener.accept()
            except TimeoutError:
                continue
            raw.settimeout(5)
            try:
                with context.wrap_socket(raw, server_side=True) as conn:
                    protocol = conn.selected_alpn_protocol() or "http/1.1"
                    protocols.append(protocol)
                    (answer_h2 if protocol == "h2" else answer_h1)(conn)
            except (OSError, ssl.SSLError):
                pass

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    try:
        yield f"https://localhost:{listener.getsockname()[1]}", protocols
    finally:
        stop.set()
        thread.join()
        listener.close()


def test_chat_repeated_headers_sent_separately():
    with http_fake(lambda request: (418, "")) as (base_url, received):
        status = gateway.chat(
            Endpoint(base_url, True),
            "mock/small",
            "key-123",
            extra_headers=[("x-journey-team", "team-a"), ("x-journey-team", "team-b"), ("x-other", "1")],
        )
    assert status == 418
    [request] = received
    assert (request.method, request.path) == ("POST", CHAT_PATH)
    assert request.header_values("Authorization") == ["Bearer key-123"]
    assert request.header_values("x-journey-team") == ["team-a", "team-b"]
    assert request.header_values("x-other") == ["1"]


@pytest.mark.parametrize("size", [120, 4096, 3 * 1024 * 1024])
def test_chat_body_bytes_exact(size):
    with http_fake(ok) as (base_url, received):
        assert gateway.chat(Endpoint(base_url, True), "mock/small", "key", body_bytes=size) == 200
    [request] = received
    assert len(request.body) == size
    assert request.header_values("Content-Length") == [str(size)]
    assert request.header_values("Content-Type") == ["application/json"]
    body = json.loads(request.body)
    assert body["model"] == "mock/small"
    assert body["messages"][0]["role"] == "user"


def test_chat_default_body_is_a_chat_request():
    with http_fake(ok) as (base_url, received):
        gateway.chat(Endpoint(base_url, True), "mock/small", "key")
    body = json.loads(received[0].body)
    assert body["model"] == "mock/small"
    assert body["messages"][0]["content"]


def test_chat_body_bytes_too_small_raises():
    with pytest.raises(ValueError, match="body_bytes"):
        gateway.chat(Endpoint("http://127.0.0.1:9", True), "mock/small", "key", body_bytes=10)


def test_chat_http2_flag(tmp_path):
    cert, key = self_signed_cert(tmp_path)
    with h2_fake(cert, key) as (base_url, protocols):
        endpoint = Endpoint(base_url, str(cert))
        assert gateway.chat(endpoint, "mock/small", "key", http2=True) == 200
        assert gateway.chat(endpoint, "mock/small", "key", http2=False) == 200
    assert protocols == ["h2", "http/1.1"]


def test_chat_http2_not_negotiated_raises(tmp_path):
    """A server that only speaks HTTP/1.1 must not pass an HTTP/2 test by falling back."""
    cert, key = self_signed_cert(tmp_path)
    with http_fake(ok, tls=(cert, key)) as (base_url, _):
        with pytest.raises(gateway.ProtocolNotNegotiated, match="HTTP/1.1"):
            gateway.chat(Endpoint(base_url, str(cert)), "mock/small", "key", http2=True)


def test_chat_verifies_tls(tmp_path):
    cert, key = self_signed_cert(tmp_path)
    with http_fake(ok, tls=(cert, key)) as (base_url, received):
        with pytest.raises(httpx.ConnectError, match="CERTIFICATE_VERIFY_FAILED"):
            gateway.chat(Endpoint(base_url, True), "mock/small", "key")
        assert received == []
        assert gateway.chat(Endpoint(base_url, str(cert)), "mock/small", "key") == 200
    assert len(received) == 1


@pytest.fixture
def no_wait(monkeypatch):
    monkeypatch.setattr(gateway, "POLL_INTERVAL", 0)


def scripted(*steps):
    """A call that raises or returns each step in turn; records how often it ran."""
    calls = []

    def call():
        step = steps[len(calls)]
        calls.append(step)
        if isinstance(step, BaseException):
            raise step
        return step

    return call, calls


def test_wait_for_status_retries_connection_errors_only_here(no_wait):
    # wait_for_status retries a dropped connection from either HTTP library ...
    call, calls = scripted(
        httpx.ConnectError("refused"),
        httpx.RemoteProtocolError("server disconnected"),
        requests.ConnectionError("refused"),
        503,
        200,
    )
    gateway.wait_for_status(call, 200, timeout=30, description="model answers")
    assert len(calls) == 5

    # ... but not any other error ...
    call, calls = scripted(ValueError("bad body"), 200)
    with pytest.raises(ValueError, match="bad body"):
        gateway.wait_for_status(call, 200, timeout=30, description="model answers")
    assert len(calls) == 1

    # ... and chat itself does not retry: one connection, then the error.
    accepted = []
    listener = socket.create_server(("127.0.0.1", 0))

    def drop():
        conn, _ = listener.accept()
        accepted.append(conn)
        conn.close()

    thread = threading.Thread(target=drop, daemon=True)
    thread.start()
    try:
        with pytest.raises(httpx.TransportError):
            gateway.chat(Endpoint(f"http://127.0.0.1:{listener.getsockname()[1]}", True), "mock/small", "key")
        thread.join(timeout=5)
    finally:
        listener.close()
    assert len(accepted) == 1


@pytest.mark.parametrize("library", ["httpx", "requests"])
def test_wait_for_status_does_not_retry_tls_verification_failure(tmp_path, library):
    """A certificate the client does not trust will not start being trusted: fail at once."""
    cert, key = self_signed_cert(tmp_path)
    with http_fake(ok, tls=(cert, key)) as (base_url, received):
        if library == "httpx":
            call = lambda: gateway.chat(Endpoint(base_url, True), "mock/small", "key")  # noqa: E731
        else:
            call = lambda: requests.get(base_url, timeout=5).status_code  # noqa: E731
        started = time.monotonic()
        with pytest.raises((httpx.ConnectError, requests.exceptions.SSLError)):
            gateway.wait_for_status(call, 200, timeout=30, description="model answers")
        assert time.monotonic() - started < 5
    assert received == []


def test_wait_for_status_times_out_with_description_and_last_status(no_wait):
    with pytest.raises(TimeoutError, match=r"model answers: wanted HTTP 200, last HTTP 503"):
        gateway.wait_for_status(lambda: 503, 200, timeout=0.05, description="model answers")


def test_wait_for_status_times_out_naming_last_connection_error(no_wait):
    def refused():
        raise httpx.ConnectError("connection refused")

    with pytest.raises(TimeoutError, match="connection refused"):
        gateway.wait_for_status(refused, 200, timeout=0.05, description="model answers")
