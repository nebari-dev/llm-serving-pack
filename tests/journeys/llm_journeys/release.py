"""The F13 release state file: the hand-over between the two suite runs (Q35).

The `before` run writes it and the `after` run reads it. JSON, file mode
0600, at the --state path. It names the model, the user and the API key the
`before` run created, and the NIC tag that run ran on (Q91). The password and
the key's secret never appear in a repr or an error message.
"""

import json
import os
from dataclasses import asdict, dataclass, field, fields
from pathlib import Path

from llm_journeys.keys import ApiKey


@dataclass(frozen=True)
class ReleaseState:
    model_kind: str
    model_name: str
    served_name: str
    username: str
    password: str = field(repr=False)
    key: ApiKey
    nic_tag: str  # the NIC tag the `before` run ran on (Q91)


def write_state(path: Path, state: ReleaseState) -> None:
    """Write the state as JSON with mode 0600, narrowing an existing file too."""
    data = json.dumps(asdict(state), indent=2) + "\n"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        os.fchmod(f.fileno(), 0o600)
        f.write(data)


def _check_fields(obj: object, cls: type, where: str, path: Path) -> dict:
    """obj, when it is an object with exactly the fields of cls; errors name fields, never values."""
    if not isinstance(obj, dict):
        raise ValueError(f"release state file {path} is malformed: {where} is not an object")
    names = [f.name for f in fields(cls)]
    missing = [n for n in names if n not in obj]
    extra = sorted(k for k in obj if k not in names)
    if missing or extra:
        raise ValueError(
            f"release state file {path} is malformed: {where} missing {missing or 'nothing'}, "
            f"unexpected {extra or 'nothing'}"
        )
    return obj


def read_state(path: Path) -> ReleaseState:
    """Read the state written by write_state.

    Raises FileNotFoundError when the file is missing and ValueError when it
    is malformed; both name the path.
    """
    try:
        text = Path(path).read_text()
    except FileNotFoundError:
        raise FileNotFoundError(f"release state file {path} not found: run the F13 before stage first") from None
    try:
        raw = json.loads(text)
    except json.JSONDecodeError as e:
        raise ValueError(f"release state file {path} is malformed: not JSON (line {e.lineno})") from None
    top = _check_fields(raw, ReleaseState, "the file", path)
    key = _check_fields(top["key"], ApiKey, "key", path)
    for where, obj in (("the file", {k: v for k, v in top.items() if k != "key"}), ("key", key)):
        bad = sorted(k for k, v in obj.items() if not isinstance(v, str))
        if bad:
            raise ValueError(f"release state file {path} is malformed: {where} has non-string {bad}")
    return ReleaseState(**{**top, "key": ApiKey(**key)})
