"""llm_journeys.tokens: the password grant's shape, its errors, token freshness.

No cluster: a local http.server stands in for the Keycloak token endpoint.
"""

import base64
import json
from urllib.parse import parse_qs

import pytest

from llm_journeys import tokens
from llm_journeys.target import REALM_TOKEN_PATH, Endpoint
from tests_lib.fakes import http_fake, make_target, nebari_shaped

PASSWORD = "s3cret-pa55word"
NOW = 1_800_000_000.0


def jwt(exp: float, sub: str = "alice") -> str:
    def part(obj):
        return base64.urlsafe_b64encode(json.dumps(obj).encode()).decode().rstrip("=")

    return f"{part({'alg': 'RS256'})}.{part({'sub': sub, 'exp': int(exp)})}.signature"


class Clock:
    def __init__(self, now: float):
        self.now = now

    def __call__(self) -> float:
        return self.now


def minting(*tokens_in_order):
    """A token endpoint that hands out the given tokens, one per request."""
    queue = list(tokens_in_order)
    return lambda request: (200, {"access_token": queue.pop(0), "token_type": "Bearer"})


def test_password_grant_request_shape():
    token = jwt(NOW + 300)
    with http_fake(minting(token)) as (base_url, received):
        grant = tokens.PasswordGrant(Endpoint(base_url, True), "nebari-frontend-spa")
        assert grant.token("alice", PASSWORD) == token
    [request] = received
    assert (request.method, request.path) == ("POST", REALM_TOKEN_PATH)
    assert request.header_values("Content-Type") == ["application/x-www-form-urlencoded"]
    assert parse_qs(request.body.decode(), strict_parsing=True) == {
        "client_id": ["nebari-frontend-spa"],
        "username": ["alice"],
        "password": [PASSWORD],
        "grant_type": ["password"],
    }


@pytest.mark.parametrize(
    "reply",
    [
        (401, {"error": "invalid_grant", "error_description": "Invalid user credentials"}),
        (500, "internal error"),
        (200, {"access_token": ""}),
        (200, {"token_type": "Bearer"}),
        (200, "not json"),
    ],
    ids=["401", "500", "empty-token", "no-token", "not-json"],
)
def test_password_grant_error_raises(reply):
    with http_fake(lambda request: reply) as (base_url, _):
        grant = tokens.PasswordGrant(Endpoint(base_url, True), "nebari-frontend-spa")
        with pytest.raises(tokens.TokenError) as raised:
            grant.token("alice", PASSWORD)
    assert "alice" in str(raised.value)
    assert PASSWORD not in str(raised.value)
    assert PASSWORD not in repr(raised.value)


def test_token_reused_when_fresh():
    clock = Clock(NOW)
    first = jwt(NOW + 300)
    with http_fake(minting(first)) as (base_url, received):
        grant = tokens.PasswordGrant(Endpoint(base_url, True), "spa", clock=clock)
        assert grant.token("alice", PASSWORD) == first
        clock.now = NOW + 239  # 61 s left
        assert grant.token("alice", PASSWORD) == first
    assert len(received) == 1


def test_token_reminted_under_60s():
    clock = Clock(NOW)
    first, second = jwt(NOW + 300), jwt(NOW + 600)
    with http_fake(minting(first, second)) as (base_url, received):
        grant = tokens.PasswordGrant(Endpoint(base_url, True), "spa", clock=clock)
        assert grant.token("alice", PASSWORD) == first
        clock.now = NOW + 241  # 59 s left
        assert grant.token("alice", PASSWORD) == second
    assert len(received) == 2


def test_token_cached_per_user():
    alice, bob = jwt(NOW + 300, "alice"), jwt(NOW + 300, "bob")
    with http_fake(minting(alice, bob)) as (base_url, received):
        grant = tokens.PasswordGrant(Endpoint(base_url, True), "spa", clock=Clock(NOW))
        assert grant.token("alice", PASSWORD) == alice
        assert grant.token("bob", PASSWORD) == bob
        assert grant.token("alice", PASSWORD) == alice
    assert len(received) == 2


def test_seconds_left():
    assert tokens.seconds_left(jwt(NOW + 90), NOW) == 90
    assert tokens.seconds_left(jwt(NOW - 5), NOW) == -5


def test_token_source_is_password_grant_where_set():
    source = tokens.token_source(make_target(), new_context=lambda: pytest.fail("no browser on kind-dev"))
    assert isinstance(source, tokens.PasswordGrant)


def test_token_source_without_password_grant_is_not_built_yet():
    with pytest.raises(NotImplementedError, match="browser login"):
        tokens.token_source(nebari_shaped(), new_context=lambda: pytest.fail("not opened"))
