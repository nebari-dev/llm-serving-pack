"""Tests for the session sweep in llm_journeys.models. No cluster: fake kube and Keycloak objects."""

import pytest
from kubernetes.client.rest import ApiException

from llm_journeys import models
from llm_journeys.keys import ApiKey
from llm_journeys.release import ReleaseState, write_state
from tests_lib.fakes import FakeKeycloak, FakeObjects, fake_cluster, make_target

NS = "llm-operator-system"
LLM = ("llm.nebari.dev", "llmmodels")
PASSTHROUGH = ("llm.nebari.dev", "passthroughmodels")
ROUTES = ("aigateway.envoyproxy.io", "aigatewayroutes")
POLICIES = ("gateway.envoyproxy.io", "securitypolicies")
POOLS = ("inference.networking.k8s.io", "inferencepools")
JOURNEY = {"nebari.dev/test-journey": "true"}


def model(name: str, labels: dict | None = None) -> dict:
    return {"metadata": {"name": name, "labels": dict(labels or {})}}


def owned(name: str, model_name: str) -> dict:
    """An object the operator made for model_name: it carries the model label only."""
    return model(name, {"llm.nebari.dev/model": model_name, "app.kubernetes.io/managed-by": "nebari-llm-operator"})


def add_model_objects(objects: FakeObjects, model_name: str) -> None:
    for gp, suffix in ((ROUTES, "-external"), (ROUTES, "-internal"), (POLICIES, "-external"), (POOLS, "")):
        objects.add(*gp, NS, owned(model_name + suffix, model_name))


def user(username: str, *, email_domain: str = "journeys.invalid") -> dict:
    return {"id": "id-" + username, "username": username, "email": f"{username}@{email_domain}"}


def held_state(path, *, model_name: str, username: str) -> None:
    write_state(
        path,
        ReleaseState(
            model_kind="LLMModel",
            model_name=model_name,
            served_name="test/" + model_name,
            username=username,
            password="before-run-password",
            key=ApiKey(namespace=NS, model=model_name, client_id="cid", secret="before-run-secret"),
            nic_tag="v0.18.0",
        ),
    )


def test_sweep_skips_f13_held_objects(tmp_path, monkeypatch):
    default_state = tmp_path / "results" / "f13-state.json"
    default_state.parent.mkdir()
    monkeypatch.setattr(models, "DEFAULT_STATE_PATH", default_state)
    held_state(default_state, model_name="journey-held", username="journey-heldu")

    objects = FakeObjects()
    objects.add(*LLM, NS, model("journey-held", JOURNEY))
    objects.add(*LLM, NS, model("journey-stale", JOURNEY))
    objects.add(*PASSTHROUGH, NS, model("journey-stalep", JOURNEY))
    add_model_objects(objects, "journey-held")
    keycloak = FakeKeycloak([user("journey-heldu"), user("journey-staleu"), user("alice", email_domain="example.com")])
    target = make_target(cluster=fake_cluster(custom=objects), keycloak=keycloak)

    results = models.sweep_leftovers(target, interval=0)

    assert objects.names(*LLM, NS) == ["journey-held"]
    assert objects.names(*PASSTHROUGH, NS) == []
    assert objects.names(*ROUTES, NS) == ["journey-held-external", "journey-held-internal"]
    assert objects.names(*POLICIES, NS) == ["journey-held-external"]
    assert objects.names(*POOLS, NS) == ["journey-held"]
    assert keycloak.usernames() == ["alice", "journey-heldu"]
    assert sorted(results["models"].deleted) == ["LLMModel/journey-stale", "PassthroughModel/journey-stalep"]
    assert results["users"].deleted == ["journey-staleu"]
    assert not results["models"].failed and not results["users"].failed


def test_sweep_without_a_state_file_deletes_every_stale_object(tmp_path):
    objects = FakeObjects()
    objects.add(*LLM, NS, model("journey-a", JOURNEY))
    keycloak = FakeKeycloak([user("journey-u")])
    target = make_target(cluster=fake_cluster(custom=objects), keycloak=keycloak)

    models.sweep_leftovers(target, tmp_path / "absent.json", interval=0)

    assert objects.names(*LLM, NS) == []
    assert keycloak.usernames() == []


def test_sweep_refuses_a_malformed_state_file(tmp_path):
    state = tmp_path / "f13-state.json"
    state.write_text("{not json")
    objects = FakeObjects()
    objects.add(*LLM, NS, model("journey-a", JOURNEY))
    target = make_target(cluster=fake_cluster(custom=objects), keycloak=FakeKeycloak())

    with pytest.raises(ValueError, match="f13-state.json"):
        models.sweep_leftovers(target, state, interval=0)
    assert objects.names(*LLM, NS) == ["journey-a"]  # nothing deleted on a state it cannot read


def test_sweep_reports_a_labelled_model_outside_the_prefix_as_skipped(tmp_path):
    objects = FakeObjects()
    objects.add(*LLM, NS, model("prod-model", JOURNEY))
    target = make_target(cluster=fake_cluster(custom=objects), keycloak=FakeKeycloak())

    result = models.sweep_stale_models(target, interval=0)

    assert result.skipped == ["LLMModel/prod-model"]
    assert objects.names(*LLM, NS) == ["prod-model"]


def test_sweep_deletes_issue_194_leftovers_of_deleted_journey_models_only():
    """Issue #194: routes, policies and pools outlive their model; only a journey model's are removed."""
    objects = FakeObjects()
    # A stale journey model: the sweep deletes it and then what the operator left of it.
    objects.add(*LLM, NS, model("journey-stale", JOURNEY))
    add_model_objects(objects, "journey-stale")
    # A journey model deleted by an earlier run: only its leftovers remain.
    add_model_objects(objects, "journey-gone")
    # A journey-named model that still exists without the test label: not stale, so its objects stay.
    objects.add(*LLM, NS, model("journey-live"))
    add_model_objects(objects, "journey-live")
    # Not journey models: never touched, present or not.
    objects.add(*LLM, NS, model("test-model"))
    add_model_objects(objects, "test-model")
    add_model_objects(objects, "deleted-prod-model")
    # Another namespace: out of scope.
    objects.add(*ROUTES, "other-ns", owned("journey-gone-external", "journey-gone"))
    # Unlabelled object with a journey-like name: not the operator's, not touched.
    objects.add(*ROUTES, NS, model("journey-gone-handmade"))
    target = make_target(cluster=fake_cluster(custom=objects), keycloak=FakeKeycloak())

    result = models.sweep_stale_models(target, interval=0)

    assert objects.names(*LLM, NS) == ["journey-live", "test-model"]
    assert objects.names(*ROUTES, NS) == [
        "deleted-prod-model-external",
        "deleted-prod-model-internal",
        "journey-gone-handmade",
        "journey-live-external",
        "journey-live-internal",
        "test-model-external",
        "test-model-internal",
    ]
    assert objects.names(*POLICIES, NS) == [
        "deleted-prod-model-external",
        "journey-live-external",
        "test-model-external",
    ]
    assert objects.names(*POOLS, NS) == ["deleted-prod-model", "journey-live", "test-model"]
    assert objects.names(*ROUTES, "other-ns") == ["journey-gone-external"]
    assert "LLMModel/journey-stale" in result.deleted
    assert "AIGatewayRoute/journey-gone-external" in result.deleted
    assert "InferencePool/journey-stale" in result.deleted
    assert not result.failed and not result.skipped


def test_delete_model_removes_its_issue_194_leftovers():
    objects = FakeObjects()
    objects.add(*LLM, NS, model("journey-x", JOURNEY))
    add_model_objects(objects, "journey-x")
    add_model_objects(objects, "journey-y")
    target = make_target(cluster=fake_cluster(custom=objects))

    models.delete_model(target, "LLMModel", "journey-x", interval=0)

    assert objects.names(*LLM, NS) == []
    assert objects.names(*ROUTES, NS) == ["journey-y-external", "journey-y-internal"]
    assert objects.names(*POLICIES, NS) == ["journey-y-external"]
    assert objects.names(*POOLS, NS) == ["journey-y"]


def test_sweep_leaves_issue_194_objects_alone_when_a_model_kind_cannot_be_listed():
    """A model of the unlisted kind may be live: its routes, policies and pools must survive."""
    objects = FakeObjects()
    objects.list_errors["llmmodels"] = ApiException(status=500, reason="Internal Server Error")
    objects.add(*LLM, NS, model("journey-live", JOURNEY))
    add_model_objects(objects, "journey-live")
    add_model_objects(objects, "journey-gone")
    target = make_target(cluster=fake_cluster(custom=objects), keycloak=FakeKeycloak())

    result = models.sweep_stale_models(target, interval=0)

    assert objects.names(*ROUTES, NS) == [
        "journey-gone-external",
        "journey-gone-internal",
        "journey-live-external",
        "journey-live-internal",
    ]
    assert objects.names(*POLICIES, NS) == ["journey-gone-external", "journey-live-external"]
    assert objects.names(*POOLS, NS) == ["journey-gone", "journey-live"]
    assert result.deleted == []
    assert result.failed == ["<listing llmmodels failed: ApiException 500 Internal Server Error>"]
