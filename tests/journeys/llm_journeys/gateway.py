"""Requests through the Envoy gateway, and waiting for an expected status.

chat uses httpx because requests cannot speak HTTP/2. It sends one request
and returns its status; it never retries. wait_for_status is the only place
a connection error is retried (Q58): a dropped port-forward or a listener
that is not up yet looks like one.
"""

import json
import ssl
import time
from collections.abc import Callable, Sequence

import httpx
import requests

from llm_journeys.target import Endpoint

CHAT_PATH = "/v1/chat/completions"
REQUEST_TIMEOUT = 60
POLL_INTERVAL = 2.0
DEFAULT_PROMPT = "Say hello."

# What a dropped or not-yet-listening connection raises, from either library.
CONNECTION_ERRORS = (
    httpx.ConnectError,
    httpx.ReadError,
    httpx.WriteError,
    httpx.RemoteProtocolError,
    requests.ConnectionError,
)


class ProtocolNotNegotiated(RuntimeError):
    """HTTP/2 was asked for but the server answered over another protocol."""


def _chat_body(model: str, body_bytes: int | None) -> bytes:
    def encode(content: str) -> bytes:
        return json.dumps({"model": model, "messages": [{"role": "user", "content": content}]}).encode()

    if body_bytes is None:
        return encode(DEFAULT_PROMPT)
    padding = body_bytes - len(encode(""))
    if padding < 0:
        raise ValueError(f"body_bytes={body_bytes} is smaller than the smallest chat body ({len(encode(''))} bytes)")
    return encode("x" * padding)  # one byte per character in JSON, so the body is exactly body_bytes


def _verify(endpoint: Endpoint) -> ssl.SSLContext | bool:
    # httpx 0.28 deprecates verify=<path>; a CA bundle path becomes a context.
    if isinstance(endpoint.verify, str):
        return ssl.create_default_context(cafile=endpoint.verify)
    return endpoint.verify


def chat(
    endpoint: Endpoint,
    model: str,
    bearer: str,
    *,
    extra_headers: Sequence[tuple[str, str]] = (),  # a sequence, so a header can repeat
    body_bytes: int | None = None,
    http2: bool = False,
) -> int:
    """POST one chat completion and return the HTTP status."""
    body = _chat_body(model, body_bytes)
    headers = [("Authorization", f"Bearer {bearer}"), ("Content-Type", "application/json"), *extra_headers]
    with httpx.Client(http1=True, http2=http2, verify=_verify(endpoint), timeout=REQUEST_TIMEOUT) as client:
        response = client.post(f"{endpoint.base_url}{CHAT_PATH}", headers=headers, content=body)
    if http2 and response.http_version != "HTTP/2":
        raise ProtocolNotNegotiated(f"asked {endpoint.base_url} for HTTP/2, it answered over {response.http_version}")
    return response.status_code


def _certificate_rejected(error: BaseException) -> bool:
    """True when certificate verification is somewhere in the exception chain."""
    seen: set[int] = set()
    current: BaseException | None = error
    while current is not None and id(current) not in seen:
        if isinstance(current, ssl.SSLCertVerificationError):
            return True
        seen.add(id(current))
        current = current.__cause__ or current.__context__
    return False


def wait_for_status(call: Callable[[], int], want: int, *, timeout: float, description: str) -> None:
    """Call until it returns want; retries other statuses and connection errors, nothing else."""
    deadline = time.monotonic() + timeout
    while True:
        try:
            status = call()
            last = f"HTTP {status}"
            if status == want:
                return
        except CONNECTION_ERRORS as error:
            if _certificate_rejected(error):
                raise  # an untrusted certificate stays untrusted: not worth waiting for
            last = f"{type(error).__name__}: {error}"
        if time.monotonic() >= deadline:
            raise TimeoutError(f"{description}: wanted HTTP {want}, last {last} after {timeout:g} s")
        time.sleep(POLL_INTERVAL)
