"""AWS Stubber checks requests/responses against the installed AWS service model."""

import unittest
from datetime import datetime, timezone
from unittest.mock import MagicMock, patch
from types import SimpleNamespace

import boto3
from botocore.stub import Stubber
from botocore.exceptions import ClientError

from backends import bedrock
from base import ProviderError


def client(service):
    # Stubber intercepts all requests; these placeholders prevent credential lookup.
    return boto3.client(
        service,
        region_name="us-west-2",
        aws_access_key_id="test",
        aws_secret_access_key="test",
    )


def foundation(
    model_id, provider="Amazon", inference=None, status="ACTIVE", inputs=None
):
    return {
        "modelArn": f"arn:aws:bedrock:us-west-2::foundation-model/{model_id}",
        "modelId": model_id,
        "modelName": model_id,
        "providerName": provider,
        "inputModalities": ["TEXT"] if inputs is None else inputs,
        "outputModalities": ["TEXT"],
        "responseStreamingSupported": True,
        "inferenceTypesSupported": ["ON_DEMAND"] if inference is None else inference,
        "modelLifecycle": {"status": status},
    }


def profile(model_id, foundation_id, kind="SYSTEM_DEFINED", status="ACTIVE"):
    return {
        "inferenceProfileId": model_id,
        "inferenceProfileArn": f"arn:aws:bedrock:us-west-2:123456789012:inference-profile/{model_id}",
        "inferenceProfileName": model_id,
        "createdAt": datetime(2026, 1, 1, tzinfo=timezone.utc),
        "updatedAt": datetime(2026, 1, 1, tzinfo=timezone.utc),
        "type": kind,
        "status": status,
        "models": [
            {"modelArn": f"arn:aws:bedrock:us-east-1::foundation-model/{foundation_id}"}
        ],
    }


AVAILABLE = {
    "agreementAvailability": {"status": "AVAILABLE"},
    "authorizationStatus": "AUTHORIZED",
    "entitlementAvailability": "AVAILABLE",
    "regionAvailability": "AVAILABLE",
}
USAGE = {"inputTokens": 8, "outputTokens": 2, "totalTokens": 10}
COMPLETION = {
    "output": {"message": {"role": "assistant", "content": [{"text": "Hello!"}]}},
    "stopReason": "end_turn",
    "usage": USAGE,
    "metrics": {"latencyMs": 10},
}


class DiscoveryTests(unittest.TestCase):
    def test_paginates_profiles_and_keeps_only_active_text_invocation_targets(self):
        aws = client("bedrock")
        app = profile("my-application", "anthropic.test-v1:0", kind="APPLICATION")
        with Stubber(aws) as stub:
            stub.add_response(
                "list_foundation_models",
                {
                    "modelSummaries": [
                        foundation("amazon.test-v1:0"),
                        foundation(
                            "amazon.provisioned-v1:0", inference=["PROVISIONED"]
                        ),
                        foundation("amazon.legacy-v1:0", status="LEGACY"),
                        foundation("amazon.audio-v1:0", inputs=["AUDIO"]),
                    ]
                },
                {"byProvider": "Amazon", "byOutputModality": "TEXT"},
            )
            stub.add_response(
                "list_foundation_models",
                {
                    "modelSummaries": [
                        foundation("anthropic.test-v1:0", "Anthropic", inference=[])
                    ]
                },
                {"byProvider": "Anthropic", "byOutputModality": "TEXT"},
            )
            stub.add_response(
                "list_inference_profiles",
                {
                    "inferenceProfileSummaries": [
                        profile("us.anthropic.test-v1:0", "anthropic.test-v1:0"),
                        profile("us.amazon.legacy-v1:0", "amazon.legacy-v1:0"),
                        profile("us.missing-v1:0", "missing-v1:0"),
                    ],
                    "nextToken": "page2",
                },
                {},
            )
            stub.add_response(
                "list_inference_profiles",
                {"inferenceProfileSummaries": [app]},
                {"nextToken": "page2"},
            )
            for model_id in ["amazon.test-v1:0", "anthropic.test-v1:0"]:
                stub.add_response(
                    "get_foundation_model_availability",
                    {"modelId": model_id, **AVAILABLE},
                    {"modelId": model_id},
                )
            models = bedrock.discover(aws, ["Amazon", "Anthropic"])
            stub.assert_no_pending_responses()
        self.assertEqual(
            {m["modelId"] for m in models},
            {
                "amazon.test-v1:0",
                "us.anthropic.test-v1:0",
                app["inferenceProfileArn"],
            },
        )
        self.assertTrue(all(m["converse"] == "unverified" for m in models))
        self.assertTrue(all(m["availability"] for m in models))

    def test_availability_denial_is_visible_and_not_mistaken_for_no_models(self):
        aws = client("bedrock")
        with Stubber(aws) as stub:
            stub.add_response(
                "list_foundation_models",
                {"modelSummaries": [foundation("amazon.test-v1:0")]},
                {"byOutputModality": "TEXT"},
            )
            stub.add_response(
                "list_inference_profiles", {"inferenceProfileSummaries": []}, {}
            )
            stub.add_client_error(
                "get_foundation_model_availability",
                "AccessDeniedException",
                expected_params={"modelId": "amazon.test-v1:0"},
            )
            models = bedrock.discover(aws)
        self.assertEqual(
            models[0]["availability"]["amazon.test-v1:0"],
            {"error": "AccessDeniedException"},
        )

    def test_list_denial_fails_instead_of_generating_an_empty_manifest(self):
        aws = client("bedrock")
        with Stubber(aws) as stub:
            stub.add_client_error(
                "list_foundation_models",
                "AccessDeniedException",
                expected_params={"byOutputModality": "TEXT"},
            )
            with self.assertRaises(ClientError):
                bedrock.discover(aws)


class NativeVerificationTests(unittest.TestCase):
    def test_adapter_uses_sdk_filters_and_reports_only_the_error_code(self):
        aws = client("bedrock")
        args = SimpleNamespace(
            region="us-west-2", profile="example", publisher=["Amazon"]
        )
        with patch.object(bedrock.boto3, "Session") as session, Stubber(aws) as stub:
            session.return_value.client.return_value = aws
            stub.add_client_error(
                "list_foundation_models",
                "AccessDeniedException",
                service_message="private upstream detail",
                expected_params={"byProvider": "Amazon", "byOutputModality": "TEXT"},
            )
            adapter = bedrock.Bedrock(args)
            with self.assertRaises(ProviderError) as failure:
                adapter.discover()
            self.assertEqual(str(failure.exception), "AccessDeniedException")
            session.assert_called_once_with(
                profile_name="example", region_name="us-west-2"
            )
            self.assertEqual(adapter.config.retries["total_max_attempts"], 1)
            self.assertEqual(
                adapter.provider_spec()["credential"], {"type": "WorkloadIdentity"}
            )

    def test_converse_uses_sdk_parameters_and_rejects_incomplete_streams(self):
        runtime = client("bedrock-runtime")
        model_id = "us.meta.test-v1:0"
        request = {
            "modelId": model_id,
            "messages": bedrock.MESSAGES,
            "inferenceConfig": {"maxTokens": bedrock.MAX_TOKENS},
        }
        events = [
            {
                "contentBlockDelta": {
                    "delta": {"text": "Hello!"},
                    "contentBlockIndex": 0,
                }
            },
            {"messageStop": {"stopReason": "end_turn"}},
            {"metadata": {"usage": USAGE}},
        ]
        for stream_events, valid in [
            (events, True),
            (events[:1], False),
            ([{"throttlingException": {}}], False),
        ]:
            with self.subTest(events=stream_events), Stubber(runtime) as stub:
                stub.add_response("converse", COMPLETION, request)
                # Stubber validates Converse's AWS schema. EventStream is a
                # transport object, so supply an iterable with the same close API.
                stream = MagicMock()
                stream.__iter__.return_value = iter(stream_events)
                with patch.object(
                    runtime, "converse_stream", return_value={"stream": stream}
                ) as call:
                    if valid:
                        self.assertEqual(
                            bedrock.verify_aws(runtime, model_id)["converseStream"],
                            USAGE,
                        )
                    else:
                        with self.assertRaises(ValueError):
                            bedrock.verify_aws(runtime, model_id)
                    call.assert_called_once_with(**request)
                    stream.close.assert_called_once()
                stub.assert_no_pending_responses()


if __name__ == "__main__":
    unittest.main()
