"""Tests for llm_journeys.release, the F13 state file. These need no cluster."""

import json
import stat
from pathlib import Path

import pytest

from llm_journeys.keys import ApiKey
from llm_journeys.release import ReleaseState, read_state, write_state

PASSWORD = "pw-do-not-print-7f3a"
SECRET = "sk-do-not-print-91bc"


def _state() -> ReleaseState:
    return ReleaseState(
        model_kind="LLMModel",
        model_name="journey-f13-model",
        served_name="journey-f13",
        username="journey-f13-user",
        password=PASSWORD,
        key=ApiKey(namespace="llm-serving", model="journey-f13-model", client_id="client-123", secret=SECRET),
        nic_tag="v0.18.0",
    )


def test_state_round_trip(tmp_path: Path):
    path = tmp_path / "state.json"
    write_state(path, _state())
    assert read_state(path) == _state()


def test_state_file_mode_0600(tmp_path: Path):
    path = tmp_path / "state.json"
    path.write_text("{}")
    path.chmod(0o644)  # an existing file is narrowed too, not only a new one
    write_state(path, _state())
    assert stat.S_IMODE(path.stat().st_mode) == 0o600


def test_state_missing_file_names_path(tmp_path: Path):
    path = tmp_path / "absent.json"
    with pytest.raises(FileNotFoundError, match=str(path)):
        read_state(path)


@pytest.mark.parametrize(
    "content",
    [
        "not json",
        "[]",
        json.dumps({"model_kind": "LLMModel"}),  # fields missing
    ],
    ids=["not-json", "not-an-object", "missing-fields"],
)
def test_state_malformed_names_path(tmp_path: Path, content: str):
    path = tmp_path / "state.json"
    path.write_text(content)
    with pytest.raises(ValueError, match=str(path)):
        read_state(path)


def test_state_malformed_hides_password_and_key(tmp_path: Path):
    path = tmp_path / "state.json"
    write_state(path, _state())
    data = json.loads(path.read_text())
    data["key"]["extra"] = "x"  # a key object with an unexpected field
    path.write_text(json.dumps(data))
    with pytest.raises(ValueError) as excinfo:
        read_state(path)
    assert PASSWORD not in str(excinfo.value)
    assert SECRET not in str(excinfo.value)


def test_state_repr_hides_password_and_key():
    text = repr(_state())
    assert PASSWORD not in text
    assert SECRET not in text
    assert "journey-f13-user" in text  # the repr is still useful
