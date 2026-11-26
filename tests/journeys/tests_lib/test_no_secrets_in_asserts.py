"""No journey test may put a secret inside an assert statement.

pytest's assertion rewriting prints the value of every sub-expression of a
failing assert, including the arguments of calls, so an API key, a password
or a bearer token used inside an assert ends up in the failure message and
in junit.xml. Compute such calls into a local first and assert on the local.

This is a backstop that matches names, not a proof: a secret first copied
into a local (bearer = key.secret) and then used inside an assert is not
caught, so keep secrets out of asserts by habit too.
"""

import ast
from pathlib import Path

JOURNEYS_DIR = Path(__file__).resolve().parent.parent / "journeys"
SECRET_ATTRIBUTES = {"secret", "password"}  # ApiKey.secret, ReleaseState.password
TOKEN_CALLS = {"token", "user_token"}  # return a bearer token


def _offences(node: ast.Assert) -> list[str]:
    found = []
    for sub in ast.walk(node):
        if isinstance(sub, ast.Attribute) and sub.attr in SECRET_ATTRIBUTES:
            found.append(f".{sub.attr}")
        elif isinstance(sub, ast.Call):
            func = sub.func
            name = func.id if isinstance(func, ast.Name) else func.attr if isinstance(func, ast.Attribute) else None
            if name in TOKEN_CALLS:
                found.append(f"{name}(...)")
    return found


def secrets_in_asserts(paths) -> list[str]:
    """file:line: what, for every assert statement that touches a secret."""
    problems = []
    for path in sorted(paths):
        tree = ast.parse(Path(path).read_text(), filename=str(path))
        for node in ast.walk(tree):
            if isinstance(node, ast.Assert):
                problems += [f"{path}:{node.lineno}: {what} inside an assert" for what in _offences(node)]
    return problems


def test_checker_finds_each_kind_of_secret(tmp_path):
    source = tmp_path / "test_x.py"
    source.write_text(
        "def test():\n"
        "    assert chat(key.secret) == 403\n"
        "    assert state.password\n"
        "    assert chat(token(user)) == 200\n"
        "    assert chat(fixtures.user_token(u, p)) == 200\n"
        "    status = chat(key.secret)\n"
        "    assert status == 403\n"
    )
    assert secrets_in_asserts([source]) == [
        f"{source}:2: .secret inside an assert",
        f"{source}:3: .password inside an assert",
        f"{source}:4: token(...) inside an assert",
        f"{source}:5: user_token(...) inside an assert",
    ]


def test_journey_tests_keep_secrets_out_of_asserts():
    paths = list(JOURNEYS_DIR.glob("*.py"))
    assert paths, f"no journey test files under {JOURNEYS_DIR}"
    problems = secrets_in_asserts(paths)
    assert not problems, "compute into a local, then assert on it:\n  " + "\n  ".join(problems)
