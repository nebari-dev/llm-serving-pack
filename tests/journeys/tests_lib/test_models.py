"""Tests for llm_journeys.models: manifests, condition parsing and the webhook retry. No cluster."""

import pytest
from kubernetes.client.rest import ApiException

from llm_journeys import models
from tests_lib.fakes import FakeCore, FakeObjects, fake_cluster, make_target

NS = "llm-operator-system"


def objects_target(objects: FakeObjects, **overrides):
    return make_target(cluster=fake_cluster(custom=objects), **overrides)


def webhook_refusal() -> ApiException:
    error = ApiException(status=500, reason="Internal Server Error")
    error.body = (
        '{"kind":"Status","status":"Failure","message":"Internal error occurred: failed calling webhook '
        '\\"vllmmodel-v1alpha1.kb.io\\": failed to call webhook: Post \\"https://llm-operator-webhook-service.'
        'llm-operator-system.svc:443/validate\\": dial tcp 10.96.0.1:443: connect: connection refused",'
        '"reason":"InternalError","code":500}'
    )
    return error


def test_new_model_name_is_journey_prefixed_and_unique():
    names = {models.new_model_name() for _ in range(20)}
    assert len(names) == 20
    assert all(name.startswith("journey-") for name in names)


def test_mock_llm_model_manifest():
    target = make_target(mock_vllm_image="mock-vllm:abc123")
    manifest = models.mock_llm_model(
        target, "journey-1a2b3c4d", "test/journey-1a2b3c4d", public=False, groups=["llm-users"]
    )

    assert manifest["apiVersion"] == "llm.nebari.dev/v1alpha1"
    assert manifest["kind"] == "LLMModel"
    assert manifest["metadata"]["name"] == "journey-1a2b3c4d"
    assert manifest["metadata"]["namespace"] == target.namespace
    assert manifest["metadata"]["labels"] == {"nebari.dev/test-journey": "true"}
    assert manifest["spec"]["model"]["name"] == "test/journey-1a2b3c4d"
    assert manifest["spec"]["serving"]["image"] == "mock-vllm:abc123"
    assert manifest["spec"]["serving"]["command"] == ["python", "/server.py"]
    assert manifest["spec"]["access"] == {"public": False, "groups": ["llm-users"]}
    assert manifest["spec"]["endpoints"]["external"]["enabled"] is True
    assert manifest["spec"]["endpoints"]["internal"]["enabled"] is True

    public = models.mock_llm_model(target, "journey-5e6f7a8b", "test/x", public=True)
    assert public["spec"]["access"] == {"public": True}


@pytest.mark.parametrize("name", ["test-model", "prod-journey-1", "journey"])
def test_mock_llm_model_refuses_a_name_outside_the_journey_prefix(name):
    with pytest.raises(ValueError, match="journey-"):
        models.mock_llm_model(make_target(), name, "test/x", public=True)


def test_mock_llm_model_needs_the_mock_image():
    with pytest.raises(ValueError, match="mock vLLM image"):
        models.mock_llm_model(make_target(mock_vllm_image=""), "journey-1", "test/x", public=True)


def test_endpoint_conditions_parse():
    obj = {
        "status": {
            "phase": "Ready",
            "conditions": [
                {"type": "Ready", "status": "True", "reason": "Reconciled"},
                {"type": "ExternalEndpointReady", "status": "True", "reason": "Applied"},
                {"type": "InternalEndpointReady", "status": "Unknown", "reason": "RoutePending"},
            ],
        }
    }
    assert models.endpoint_conditions(obj) == {
        "ExternalEndpointReady": ("True", "Applied"),
        "InternalEndpointReady": ("Unknown", "RoutePending"),
    }
    assert models.endpoint_conditions({}) == {}
    assert models.endpoint_conditions({"status": {"conditions": None}}) == {}


def test_apply_model_retries_webhook_refusal():
    objects = FakeObjects()
    objects.create_errors = [webhook_refusal(), webhook_refusal()]
    target = objects_target(objects)
    manifest = models.mock_llm_model(target, "journey-0001", "test/one", public=True)

    models.apply_model(target, manifest, interval=0)

    assert objects.creates == 3
    assert objects.names("llm.nebari.dev", "llmmodels", NS) == ["journey-0001"]


def test_apply_model_raises_other_refusals_at_once():
    objects = FakeObjects()
    denied = ApiException(status=400, reason="Bad Request")
    denied.body = '{"message":"admission webhook \\"vllmmodel-v1alpha1.kb.io\\" denied the request: bad spec"}'
    objects.create_errors = [denied]
    target = objects_target(objects)
    manifest = models.mock_llm_model(target, "journey-0002", "test/two", public=True)

    with pytest.raises(ApiException) as raised:
        models.apply_model(target, manifest, interval=0)
    assert raised.value.status == 400
    assert objects.creates == 1


def test_apply_model_gives_up_after_its_timeout():
    objects = FakeObjects()
    objects.create_errors = [webhook_refusal() for _ in range(1000)]
    target = objects_target(objects)
    manifest = models.mock_llm_model(target, "journey-0003", "test/three", public=True)

    with pytest.raises(TimeoutError, match="admission webhook"):
        models.apply_model(target, manifest, timeout=0.05, interval=0.01)


def test_wait_ready_returns_the_ready_object():
    objects = FakeObjects()
    objects.add("llm.nebari.dev", "llmmodels", NS, {"metadata": {"name": "journey-r"}, "status": {"phase": "Ready"}})
    obj = models.wait_ready(objects_target(objects), "LLMModel", "journey-r", timeout=1, interval=0)
    assert obj["metadata"]["name"] == "journey-r"


def test_wait_ready_timeout_names_the_last_phase():
    objects = FakeObjects()
    objects.add(
        "llm.nebari.dev", "llmmodels", NS, {"metadata": {"name": "journey-p"}, "status": {"phase": "Pending"}}
    )
    with pytest.raises(TimeoutError, match="journey-p.*Pending"):
        models.wait_ready(objects_target(objects), "LLMModel", "journey-p", timeout=0.05, interval=0.01)


def test_wait_condition_matches_status_and_reason():
    objects = FakeObjects()
    objects.add(
        "llm.nebari.dev",
        "passthroughmodels",
        NS,
        {
            "metadata": {"name": "journey-c"},
            "status": {
                "conditions": [{"type": "InternalEndpointReady", "status": "False", "reason": "RouteNotAccepted"}]
            },
        },
    )
    target = objects_target(objects)
    obj = models.wait_condition(
        target, "PassthroughModel", "journey-c", "InternalEndpointReady", status="False", reason="RouteNotAccepted",
        timeout=1, interval=0,
    )
    assert obj["metadata"]["name"] == "journey-c"
    with pytest.raises(TimeoutError, match="InternalEndpointReady.*False/RouteNotAccepted"):
        models.wait_condition(
            target, "PassthroughModel", "journey-c", "InternalEndpointReady", status="True", reason="Applied",
            timeout=0.05, interval=0.01,
        )


def test_unknown_kind_is_refused():
    with pytest.raises(ValueError, match="kind"):
        models.wait_ready(make_target(), "Deployment", "journey-x", timeout=0)


def test_inject_api_key_adds_the_client_to_the_model_secret():
    core = FakeCore(secrets={(NS, "journey-k-api-keys"): {}})
    models.inject_api_key(make_target(cluster=fake_cluster(core=core)), "journey-k", "client-a", lambda: "key-a-value")
    assert core.secrets[(NS, "journey-k-api-keys")] == {"client-a": "key-a-value"}


def test_inject_api_key_failure_never_names_the_key():
    target = make_target(cluster=fake_cluster(core=FakeCore()))
    with pytest.raises(RuntimeError, match="journey-k-api-keys.*404") as raised:
        models.inject_api_key(target, "journey-k", "client-a", lambda: "key-a-value")
    assert "key-a-value" not in str(raised.value)
    assert raised.value.__cause__ is None and raised.value.__suppress_context__
