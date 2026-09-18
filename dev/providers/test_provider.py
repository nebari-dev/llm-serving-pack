"""A second, test-only adapter exercises the workflow without AWS settings."""

import io
import json
import os
import unittest
from unittest.mock import MagicMock, patch

import provider
from base import ProviderError
from catalog import manifest
from gateway import gateway_credentials


class ExampleProvider:
    @staticmethod
    def add_arguments(parser):
        parser.add_argument("--tenant", required=True)

    def __init__(self, args):
        self.tenant = args.tenant

    def provider_spec(self):
        return {
            "backend": {"type": "Example", "example": {"tenant": self.tenant}},
            "credential": {"type": "WorkloadIdentity"},
        }

    def discover(self):
        return [{"modelId": "example/model-a"}, {"modelId": "example/model-b"}]

    def verify_native(self, model_id):
        if model_id == "denied":
            raise ProviderError("AccessDenied")
        return {"completion": "verified", "stream": "verified"}


class ProviderWorkflowTests(unittest.TestCase):
    def run_cli(self, *args):
        with (
            patch.dict(provider.BACKENDS, {"example": ExampleProvider}, clear=True),
            patch("sys.stdout", new=io.StringIO()) as output,
        ):
            status = provider.main(["--backend", "example", "--tenant", "team", *args])
        return status, output.getvalue()

    def test_discovery_and_manifest_need_no_aws_fields(self):
        status, output = self.run_cli("discover")
        result = json.loads(output)
        self.assertEqual(status, 0)
        self.assertEqual(result["provider"]["backend"]["example"], {"tenant": "team"})
        self.assertEqual(len(result["models"]), 2)
        status, output = self.run_cli(
            "manifest",
            "--name",
            "example",
            "--group",
            "team",
            "--model-id",
            "example/model-b",
        )
        resource = json.loads(output)
        self.assertEqual(status, 0)
        self.assertEqual(resource["spec"]["provider"], result["provider"])
        self.assertEqual(resource["spec"]["models"]["declared"], ["example/model-b"])

    def test_manifest_selects_catalog_ids_without_mutating_adapter_config(self):
        spec = {
            "backend": {"type": "Example"},
            "credential": {"type": "APIKey"},
            "credentialSecretName": "upstream",
        }
        resource = manifest(
            spec, "test", "llm", ["team"], ["b", "b"], [{"modelId": "b"}]
        )
        self.assertEqual(resource["spec"]["models"]["declared"], ["b"])
        resource["spec"]["provider"]["backend"]["type"] = "changed"
        self.assertEqual(spec["backend"]["type"], "Example")
        with self.assertRaisesRegex(ValueError, "absent"):
            manifest(spec, "test", "llm", ["team"], ["missing"], [])

    def test_native_failure_does_not_hide_other_models(self):
        status, output = self.run_cli(
            "verify", "--model-id", "denied", "--model-id", "available"
        )
        rows = [json.loads(line) for line in output.splitlines()]
        self.assertEqual(status, 1)
        self.assertEqual(rows[0]["native"], {"error": "AccessDenied"})
        self.assertEqual(rows[1]["native"]["stream"], "verified")
        self.assertTrue(all(row["gateway"] == "not tested" for row in rows))

    def test_each_gateway_is_checked_even_after_native_or_external_failure(self):
        adapter = ExampleProvider(type("Args", (), {"tenant": "team"})())
        endpoints = [
            ("external", "https://external.example", "key", "wrong-key"),
            ("internal", "https://internal.example", "jwt", "wrong-jwt"),
        ]
        with patch.object(
            provider,
            "verify_gateway",
            side_effect=[ValueError("denied"), {"stream": "verified"}],
        ) as check:
            result, failed = provider.verify(adapter, "denied", endpoints)
        self.assertTrue(failed)
        self.assertIn("error", result["native"])
        self.assertIn("error", result["gateway"]["external"])
        self.assertEqual(result["gateway"]["internal"], {"stream": "verified"})
        self.assertEqual(check.call_count, 2)

    def test_gateway_configuration_is_checked_before_adapter_creation(self):
        adapter = MagicMock()
        for urls in (
            ["--external-url", "https://external.example"],
            [
                "--external-url",
                "https://external.example",
                "--internal-url",
                "https://internal.example",
            ],
        ):
            with (
                patch.dict(provider.BACKENDS, {"example": adapter}, clear=True),
                patch.dict(os.environ, {}, clear=True),
                patch("sys.stderr", new=io.StringIO()),
            ):
                status = provider.main(
                    ["--backend", "example", "verify", "--model-id", "a", *urls]
                )
            self.assertEqual(status, 1)
            adapter.assert_not_called()

    def test_gateway_credentials_have_no_backend_specific_names(self):
        args = type(
            "Args",
            (),
            {
                "external_url": "https://external.example",
                "internal_url": "https://internal.example",
            },
        )()
        with patch.dict(
            os.environ,
            {
                "PROVIDER_TEST_API_KEY": "key",
                "PROVIDER_TEST_DENIED_API_KEY": "wrong-key",
                "PROVIDER_TEST_JWT": "jwt",
                "PROVIDER_TEST_DENIED_JWT": "wrong-jwt",
            },
            clear=True,
        ):
            endpoints = gateway_credentials(args)
        self.assertEqual(
            [entry[2:] for entry in endpoints],
            [("key", "wrong-key"), ("jwt", "wrong-jwt")],
        )
