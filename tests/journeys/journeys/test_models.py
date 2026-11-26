"""F2 and F9: models reach Ready and serve through the gateway.

Issue #283: a model can report Ready before its endpoint picker exists, so
the gateway may answer 500 for a while after Ready. Every request to a
model therefore first waits (bounded) for the expected status with
gateway.wait_for_status, then asserts on a fresh request.
"""

import pytest

from llm_journeys import gateway, models

# How long a fresh model or key may take to converge in the gateway: the
# listener, Envoy Gateway's Secret sync, the allow-list and the endpoint
# picker (issue #283). test-model-scope.sh allowed about 4 minutes.
CONVERGE_TIMEOUT = 240
CONDITION_TIMEOUT = 120  # an endpoint condition is Unknown/RoutePending until the gateway accepts the route
PROFILE_MODEL_TIMEOUT = 60  # the profile's PassthroughModel is Ready before the run starts


@pytest.mark.feature("F2")
@pytest.mark.integration
@pytest.mark.journey
def test_llmmodel_ready_with_both_endpoint_conditions(target, scratch_model):
    name = scratch_model()["metadata"]["name"]
    # Ready does not wait for route acceptance: an endpoint condition may still
    # be Unknown (RoutePending), which is not a failure, until the gateway
    # accepts the route. Wait for each to turn True, then assert on a fresh read.
    for condition in models.ENDPOINT_CONDITIONS:
        models.wait_condition(target, "LLMModel", name, condition, status="True", reason="Applied",
                              timeout=CONDITION_TIMEOUT)

    model = target.cluster.custom.get_namespaced_custom_object(
        group=models.GROUP, version=models.VERSION, namespace=target.namespace,
        plural=models.PLURALS["LLMModel"], name=name,
    )
    assert model["status"]["phase"] == "Ready"
    conditions = models.endpoint_conditions(model)
    for condition in models.ENDPOINT_CONDITIONS:
        assert conditions.get(condition) == ("True", "Applied"), f"{condition}: {conditions.get(condition)}"


@pytest.mark.feature("F9")
@pytest.mark.integration
@pytest.mark.journey
def test_passthrough_model_serves_through_mock_provider(target, api_key):
    name = target.mock_provider_model
    model = models.wait_ready(target, "PassthroughModel", name, timeout=PROFILE_MODEL_TIMEOUT)
    assert model["status"]["phase"] == "Ready"
    served = model["spec"]["models"]["declared"][0]

    key = api_key(name)

    def send() -> int:
        return gateway.chat(target.external, served, key.secret)

    gateway.wait_for_status(send, 200, timeout=CONVERGE_TIMEOUT,
                            description=f"a fresh key for PassthroughModel {name} on the external endpoint")
    assert send() == 200
