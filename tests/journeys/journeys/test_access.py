"""F5 to F8: who may call a model, on which endpoint, with what credential.

F8 ports dev/test-model-scope.sh:201-214. Its denials (403, 401) come from
the gateway's authorization filter before any backend is reached, so a
denial means something only once the positive controls pass: each test
first waits (bounded) for every control to return 200, which also covers
issue #283 (a fresh model can answer 500 for a while after Ready), and only
then asserts. A status that never arrives fails the test.
"""

import re
from collections.abc import Callable

import pytest

from llm_journeys import gateway
from llm_journeys.keys import KeyManagerClient, KeyManagerError

CONVERGE_TIMEOUT = 240  # see test_models.CONVERGE_TIMEOUT
CLIENT_ID_HEADER = "x-llm-client-id"
INVALID_API_KEY = "not-a-real-key"  # dev/test-model-scope.sh:204
# KeyManagerClient's message for handler.go:167-168 (http.Error adds the newline it strips).
OUTSIDER_MINT_REFUSED = r"POST /api/keys returned HTTP 403, want 201: forbidden: no access to model"


def _served(model: dict) -> str:
    return model["spec"]["model"]["name"]


def _sender(endpoint, model: dict, bearer: Callable[[], str]) -> Callable[[], int]:
    """A zero-argument chat call; the bearer stays in a closure, so no traceback frame shows it as an argument."""
    return lambda: gateway.chat(endpoint, _served(model), bearer())


def _converged(send: Callable[[], int], description: str) -> None:
    """Wait until send() returns 200, then assert a fresh call does."""
    gateway.wait_for_status(send, 200, timeout=CONVERGE_TIMEOUT, description=description)
    assert send() == 200, description


@pytest.mark.feature("F5")
@pytest.mark.integration
@pytest.mark.journey
def test_external_endpoint_with_api_key(target, scratch_model, api_key):
    model = scratch_model()
    key = api_key(model["metadata"]["name"])
    _converged(_sender(target.external, model, lambda: key.secret), "an API key on the external endpoint")


@pytest.mark.feature("F6")
@pytest.mark.integration
@pytest.mark.journey
def test_internal_endpoint_with_token(target, scratch_model, scratch_user, user_token):
    model = scratch_model(public=False, groups=[target.llm_group])
    username, password = scratch_user(groups=[target.llm_group])
    _converged(_sender(target.internal, model, lambda: user_token(username, password)),
               f"a token of a {target.llm_group} member on the internal endpoint")


@pytest.mark.feature("F7")
@pytest.mark.integration
@pytest.mark.journey
def test_group_restricted_model_denies_outsider(target, scratch_model, scratch_user, user_token):
    model = scratch_model(public=False, groups=[target.llm_group])
    name = model["metadata"]["name"]
    member = scratch_user(groups=[target.llm_group])
    outsider = scratch_user()  # in no group

    def token(user):
        return user_token(*user)

    # Positive control: the member gets through, so the route and its group rule are in place.
    _converged(_sender(target.internal, model, lambda: token(member)),
               f"a token of a {target.llm_group} member (control)")

    status = gateway.chat(target.internal, _served(model), token(outsider))
    assert status == 403, "an outsider's token on the internal endpoint"

    member_keys = KeyManagerClient(target.key_manager, lambda: token(member))
    outsider_keys = KeyManagerClient(target.key_manager, lambda: token(outsider))
    assert name in {m["Name"] for m in member_keys.models()}  # control: the listing has it for a member
    assert name not in {m["Name"] for m in outsider_keys.models()}

    # key-manager/internal/api/handler.go:158-170: no access to the model is
    # 403 "forbidden: no access to model". A key minted by mistake is revoked
    # before the test fails, so nothing lingers.
    try:
        minted = outsider_keys.create_key(name, "journey test key")
    except KeyManagerError as error:
        message = str(error)
        assert re.fullmatch(OUTSIDER_MINT_REFUSED, message), message
    else:
        outsider_keys.revoke_key(minted)
        pytest.fail(f"an outsider minted key {minted.client_id} for {name}; it has been revoked")


# F8, integration only: per-model key scoping and x-llm-client-id spoofing.


@pytest.fixture
def two_models(target, scratch_model, api_key):
    """(model A, key A, model B, key B): two public models, each with a key minted for it, both converged."""
    model_a, model_b = scratch_model(), scratch_model()
    key_a = api_key(model_a["metadata"]["name"])
    key_b = api_key(model_b["metadata"]["name"])
    _converged(_sender(target.external, model_a, lambda: key_a.secret), "A's key to model A (control)")
    _converged(_sender(target.external, model_b, lambda: key_b.secret), "B's key to model B (control)")
    return model_a, key_a, model_b, key_b


@pytest.mark.feature("F8")
@pytest.mark.integration
def test_key_scope_own_model_allowed(target, two_models):
    model_a, key_a, model_b, key_b = two_models
    status_a = gateway.chat(target.external, _served(model_a), key_a.secret)
    status_b = gateway.chat(target.external, _served(model_b), key_b.secret)
    assert status_a == 200, "A's key to model A"
    assert status_b == 200, "B's key to model B"


@pytest.mark.feature("F8")
@pytest.mark.integration
def test_key_scope_cross_model_denied(target, two_models):
    _, key_a, model_b, _ = two_models
    status = gateway.chat(target.external, _served(model_b), key_a.secret)
    assert status == 403, "A's key to model B"


@pytest.mark.feature("F8")
@pytest.mark.integration
def test_invalid_api_key_rejected(target, scratch_model, api_key):
    model_b = scratch_model()
    key_b = api_key(model_b["metadata"]["name"])
    _converged(_sender(target.external, model_b, lambda: key_b.secret), "B's key to model B (control)")
    status = gateway.chat(target.external, _served(model_b), INVALID_API_KEY)
    assert status == 401, "an invalid key to model B"


# dev/test-model-scope.sh:207-214: A's key to model B with a client-supplied
# x-llm-client-id naming B. The gateway must replace it with A's own ID.
SPOOFS = {
    "single": lambda a, b: [(CLIENT_ID_HEADER, b)],
    "duplicate": lambda a, b: [(CLIENT_ID_HEADER, b), (CLIENT_ID_HEADER, b)],
    "mixed-case": lambda a, b: [("X-LLM-CLIENT-ID", b)],
    "comma-joined": lambda a, b: [(CLIENT_ID_HEADER, f"{a},{b}")],
}


@pytest.mark.feature("F8")
@pytest.mark.integration
@pytest.mark.parametrize("spoof", list(SPOOFS))
def test_client_id_spoof_rejected(target, two_models, spoof):
    _, key_a, model_b, key_b = two_models
    headers = SPOOFS[spoof](key_a.client_id, key_b.client_id)
    status = gateway.chat(target.external, _served(model_b), key_a.secret, extra_headers=headers)
    assert status == 403, f"A's key to model B with a {spoof} {CLIENT_ID_HEADER} spoof"
