"""No secret in a failure's traceback (C1): not on stdout, not in the JUnit file.

pytest's long traceback styles print each frame's arguments. Each inner test
below fails the way a suite test can fail while a secret is in flight, and
runs under the suite's pytest.ini (--tb=short). The run is repeated with
--tb=auto, which prints the arguments of the first and last frames, to show
that the code also keeps secrets out of those frames' arguments.
"""

import shutil
import textwrap
from pathlib import Path

import pytest

SUITE = Path(__file__).resolve().parent.parent

API_KEY = "sk-leakcheck-apikey-7d1e9c"
PASSWORD = "pw-leakcheck-password-3b8f"

INNER = textwrap.dedent(
    f'''
    import socket
    import threading
    from types import SimpleNamespace

    import pytest
    import urllib3

    from journeys import test_access
    from llm_journeys import gateway, models
    from llm_journeys.keys import ApiKey
    from llm_journeys.target import Endpoint
    from llm_journeys.tokens import PasswordGrant
    from tests_lib.fakes import fake_cluster, http_fake, make_target

    # Module level, so no test's source listing shows them.
    API_KEY = {API_KEY!r}
    PASSWORD = {PASSWORD!r}


    def test_converged_fails_after_a_successful_wait(monkeypatch):
        statuses = iter([200, 503])
        monkeypatch.setattr(gateway, "chat", lambda endpoint, model, bearer, **kw: next(statuses))
        model = {{"metadata": {{"name": "journey-x"}}, "spec": {{"model": {{"name": "test/journey-x"}}}}}}
        target = SimpleNamespace(external=Endpoint("https://127.0.0.1:1", True))
        key = ApiKey("ns", "journey-x", "client-a", API_KEY)
        test_access.test_external_endpoint_with_api_key(target, lambda **kw: model, lambda name: key)


    def test_password_grant_answered_401():
        with http_fake(lambda request: (401, {{"error": "invalid_grant"}})) as (base_url, _):
            PasswordGrant(Endpoint(base_url, True), "spa").token("alice", PASSWORD)


    def test_password_grant_connection_dropped():
        server = socket.create_server(("127.0.0.1", 0))

        def drop():
            connection, _ = server.accept()
            connection.recv(65536)
            connection.close()

        threading.Thread(target=drop, daemon=True).start()
        try:
            PasswordGrant(Endpoint(f"http://127.0.0.1:{{server.getsockname()[1]}}", True), "spa").token(
                "alice", PASSWORD
            )
        finally:
            server.close()


    class DroppingCore:
        def patch_namespaced_secret(self, name, namespace, body):
            raise urllib3.exceptions.ProtocolError("Connection aborted.")


    def test_inject_api_key_transport_error():
        target = make_target(cluster=fake_cluster(core=DroppingCore()))
        models.inject_api_key(target, "journey-x", "client-a", lambda: API_KEY)
    '''
)


@pytest.mark.parametrize("tb", [None, "--tb=auto"], ids=["suite-pytest.ini", "tb-auto"])
def test_failures_never_print_a_secret(pytester, monkeypatch, tmp_path, tb):
    shutil.copy(SUITE / "pytest.ini", pytester.path / "pytest.ini")
    (pytester.path / "test_inner.py").write_text(INNER)
    monkeypatch.setenv("PYTHONPATH", str(SUITE))  # journeys.test_access and tests_lib.fakes
    junit = tmp_path / "junit.xml"
    args = ["-p", "no:cacheprovider", f"--junitxml={junit}", *([tb] if tb else [])]
    result = pytester.runpytest_subprocess("test_inner.py", *args)

    output = result.stdout.str() + result.stderr.str()
    report = junit.read_text()
    for secret in (API_KEY, PASSWORD):
        assert secret not in output, f"a secret is on the output of: pytest test_inner.py {' '.join(args)}"
        assert secret not in report, "a secret is in the JUnit file"
    # The failures are the expected ones, not import errors.
    result.assert_outcomes(failed=4)
    for expected in ("an API key on the external endpoint", "returned HTTP 401 (invalid_grant)",
                     "password grant for alice at", "Secret llm-operator-system/journey-x-api-keys"):
        assert expected in output, output
