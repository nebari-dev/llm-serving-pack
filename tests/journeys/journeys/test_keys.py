"""F4: the key-manager REST API and its bearer check.

Ported from dev/test-auth.sh:121-123 (the bearer checks) plus the mint,
list and revoke round trip. A revoked key reaches the gateway through
Envoy Gateway's Secret sync, so its 401 is waited for (bounded), as is the
200 of a fresh key on a fresh model (issue #283).
"""

import pytest
import requests

from llm_journeys import gateway
from llm_journeys.keys import KeyManagerClient

CONVERGE_TIMEOUT = 240  # see test_models.CONVERGE_TIMEOUT
MODELS_PATH = "/api/models"
REQUEST_TIMEOUT = 30


def _served(model: dict) -> str:
    return model["spec"]["model"]["name"]


def _client(target, user_token, user: tuple[str, str]) -> KeyManagerClient:
    username, password = user
    return KeyManagerClient(target.key_manager, lambda: user_token(username, password))


def _get_models(target, headers: dict) -> requests.Response:
    return requests.get(f"{target.key_manager.base_url}{MODELS_PATH}", headers=headers,
                        verify=target.key_manager.verify, timeout=REQUEST_TIMEOUT)


@pytest.mark.feature("F4")
@pytest.mark.integration
@pytest.mark.journey
def test_key_rest_api_mint_list_revoke(target, scratch_model, scratch_user, user_token, api_key):
    model = scratch_model()["metadata"]["name"]
    user = scratch_user(groups=[target.llm_group])
    keys = _client(target, user_token, user)

    key = api_key(model, user=user)
    listed = {k["clientId"]: k for k in keys.list_keys()}
    assert key.client_id in listed
    assert listed[key.client_id]["modelName"] == model

    keys.revoke_key(key)
    assert key.client_id not in {k["clientId"] for k in keys.list_keys()}


@pytest.mark.feature("F4")
@pytest.mark.integration
@pytest.mark.journey
def test_revoked_key_gets_401(target, scratch_model, scratch_user, user_token, api_key):
    model = scratch_model()
    user = scratch_user(groups=[target.llm_group])
    key = api_key(model["metadata"]["name"], user=user)

    def send() -> int:
        return gateway.chat(target.external, _served(model), key.secret)

    gateway.wait_for_status(send, 200, timeout=CONVERGE_TIMEOUT, description="a fresh key before revoke")
    assert send() == 200

    _client(target, user_token, user).revoke_key(key)
    gateway.wait_for_status(send, 401, timeout=CONVERGE_TIMEOUT, description="the revoked key")
    assert send() == 401


@pytest.mark.feature("F4")
@pytest.mark.integration
@pytest.mark.journey
def test_valid_bearer_lists_models(target, scratch_user, user_token):
    username, password = scratch_user(groups=[target.llm_group])
    response = _get_models(target, {"Authorization": f"Bearer {user_token(username, password)}"})
    assert response.status_code == 200
    assert isinstance(response.json()["models"], list)


@pytest.mark.feature("F4")
@pytest.mark.integration
@pytest.mark.journey
def test_missing_bearer_rejected(target):
    assert _get_models(target, {}).status_code == 401


@pytest.mark.feature("F4")
@pytest.mark.integration
@pytest.mark.journey
def test_invalid_bearer_rejected(target):
    assert _get_models(target, {"Authorization": "Bearer not.a.jwt"}).status_code == 401
