"""Root conftest: command-line options only.

Pytest only picks up pytest_addoption from a conftest at the rootdir, so
the options live here, as in NIC's tests/user_journeys. Fixtures that need
a cluster live in journeys/conftest.py, one directory down, so tests_lib/
stays runnable with no cluster.

Every option is optional here, so `pytest tests_lib` runs with no
arguments (R5-F6). --target and --kube-context are required by the
session fixtures in journeys/conftest.py, which fail when they are unset.
"""


def pytest_addoption(parser):
    parser.addoption(
        "--target",
        choices=("kind-dev", "nebari"),
        default=None,
        help="Cluster profile to test: kind-dev runs integration tests, nebari runs journey tests.",
    )
    parser.addoption(
        "--kube-context",
        default=None,
        help="Kubeconfig context to use for every cluster call; the current context is never used.",
    )
    parser.addoption(
        "--gateway-https-port",
        type=int,
        default=None,
        help="Host port the gateway is published on. Default 48443 on kind-dev, 443 on nebari.",
    )
    parser.addoption(
        "--feature",
        default=None,
        help="Comma-separated feature IDs to run, such as F4,F8. Default: every feature in the target's tiers.",
    )
    parser.addoption(
        "--release-stage",
        choices=("before", "after"),
        default=None,
        help="Run the F13 release tests for this stage instead of the journey tier. Needs --target=nebari and --state.",
    )
    parser.addoption(
        "--state",
        default=None,
        help="Path of the F13 release state file written by the before stage and read by the after stage.",
    )
    parser.addoption(
        "--keep",
        action="store_true",
        default=False,
        help="Leave each test's scratch models, users and API keys in place for debugging; "
        "the next run's session sweep removes them.",
    )
