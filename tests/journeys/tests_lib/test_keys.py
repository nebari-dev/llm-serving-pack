"""llm_journeys.keys: the key-manager REST client against a local fake.

The fake answers with the shapes key-manager/internal/api/handler.go writes.
"""

import itertools
import json

import pytest

from llm_journeys.keys import ApiKey, KeyManagerClient, KeyManagerError
from llm_journeys.target import Endpoint
from tests_lib.fakes import http_fake

SECRET = "sk-very-secret-value"
KEY_NAMESPACE = "llm-api-keys"


class FakeKeyManager:
    """In-memory key manager: one user, keys created by POST, listed by GET."""

    def __init__(self):
        self.keys: list[dict] = []

    def __call__(self, request):
        if (request.method, request.path) == ("GET", "/api/me"):
            return 200, {"username": "alice", "name": "Alice", "email": "alice@example.test", "groups": ["llm-users"]}
        if (request.method, request.path) == ("GET", "/api/models"):
            # ModelInfo has no JSON tags: PascalCase field names.
            return 200, {"models": [{"Name": "small", "Namespace": "llm-serving", "ModelName": "mock/small"}]}
        if (request.method, request.path) == ("GET", "/api/keys"):
            return 200, {"keys": self.keys}
        if (request.method, request.path) == ("POST", "/api/keys"):
            body = json.loads(request.body)
            client_id = f"client-{len(self.keys) + 1}"
            self.keys.append(
                {
                    "clientId": client_id,
                    "creator": "alice",
                    "description": body["description"],
                    "createdAt": "2026-11-26T00:00:00Z",
                    "modelName": body["modelName"],
                    "namespace": KEY_NAMESPACE,
                }
            )
            return 201, {"clientId": client_id, "apiKey": SECRET}
        if request.method == "DELETE" and request.path.startswith("/api/keys/"):
            namespace, model, client_id = request.path.removeprefix("/api/keys/").split("/")
            before = len(self.keys)
            self.keys = [
                k
                for k in self.keys
                if (k["namespace"], k["modelName"], k["clientId"]) != (namespace, model, client_id)
            ]
            return (204, "") if len(self.keys) < before else (404, "not found")
        return 404, "not found"


def client_for(base_url, token=lambda: "token-1"):
    return KeyManagerClient(Endpoint(base_url, True), token)


def test_key_manager_sends_fresh_bearer_each_request():
    counter = itertools.count(1)
    with http_fake(FakeKeyManager()) as (base_url, received):
        km = client_for(base_url, token=lambda: f"token-{next(counter)}")
        km.me()
        km.models()
        km.list_keys()
    assert [r.header_values("Authorization") for r in received] == [
        ["Bearer token-1"],
        ["Bearer token-2"],
        ["Bearer token-3"],
    ]


def test_read_verbs_return_the_handler_shapes():
    with http_fake(FakeKeyManager()) as (base_url, received):
        km = client_for(base_url)
        assert km.me()["username"] == "alice"
        assert km.models() == [{"Name": "small", "Namespace": "llm-serving", "ModelName": "mock/small"}]
        assert km.list_keys() == []
    assert [(r.method, r.path) for r in received] == [("GET", "/api/me"), ("GET", "/api/models"), ("GET", "/api/keys")]


def test_create_key_returns_apikey():
    fake = FakeKeyManager()
    with http_fake(fake) as (base_url, received):
        key = client_for(base_url).create_key("small", "journey key")
    assert key == ApiKey(namespace=KEY_NAMESPACE, model="small", client_id="client-1", secret=SECRET)
    post = received[0]
    assert (post.method, post.path) == ("POST", "/api/keys")
    assert post.header_values("Content-Type") == ["application/json"]
    assert json.loads(post.body) == {"modelName": "small", "description": "journey key"}


def test_create_key_not_in_listing_raises():
    """POST /api/keys has no namespace; a key the listing does not show cannot be revoked later."""

    def unlisted(request):
        if request.method == "POST":
            return 201, {"clientId": "client-1", "apiKey": SECRET}
        return 200, {"keys": None}

    with http_fake(unlisted) as (base_url, _):
        with pytest.raises(KeyManagerError, match="client-1") as raised:
            client_for(base_url).create_key("small")
    assert SECRET not in str(raised.value)


def test_revoke_key_deletes_by_namespace_model_client_id():
    fake = FakeKeyManager()
    with http_fake(fake) as (base_url, received):
        km = client_for(base_url)
        key = km.create_key("small")
        km.revoke_key(key)
        assert km.list_keys() == []
    assert ("DELETE", f"/api/keys/{KEY_NAMESPACE}/small/client-1") in [(r.method, r.path) for r in received]


@pytest.mark.parametrize(
    "verb",
    [
        lambda km: km.me(),
        lambda km: km.create_key("small"),
        lambda km: km.revoke_key(ApiKey(KEY_NAMESPACE, "small", "client-9", SECRET)),
    ],
    ids=["me", "create", "revoke"],
)
def test_error_status_raises_with_status(verb):
    with http_fake(lambda request: (403, "forbidden: no access to model")) as (base_url, _):
        with pytest.raises(KeyManagerError, match="HTTP 403") as raised:
            verb(client_for(base_url))
    assert SECRET not in str(raised.value)


def test_unexpected_success_status_does_not_echo_body():
    """A 2xx that is not the wanted one may carry the key; the message names status and route only."""
    with http_fake(lambda request: (200, {"clientId": "client-1", "apiKey": SECRET})) as (base_url, _):
        with pytest.raises(KeyManagerError, match="POST /api/keys returned HTTP 200, want 201") as raised:
            client_for(base_url).create_key("small")
    assert SECRET not in str(raised.value)


def test_api_key_repr_hides_secret():
    key = ApiKey(namespace=KEY_NAMESPACE, model="small", client_id="client-1", secret=SECRET)
    assert SECRET not in repr(key)
    assert SECRET not in str(key)
    assert "client-1" in repr(key)
