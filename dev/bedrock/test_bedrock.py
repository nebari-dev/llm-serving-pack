"""AWS Stubber checks requests/responses against the installed AWS service model."""

import io
import json
import os
import unittest
from datetime import datetime, timezone
from unittest.mock import MagicMock, patch

import boto3
from botocore.stub import Stubber
from botocore.exceptions import ClientError

import bedrock


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
                        foundation("anthropic.test-v1:0", "Anthropic", inference=[]),
                        foundation(
                            "amazon.provisioned-v1:0", inference=["PROVISIONED"]
                        ),
                        foundation("amazon.legacy-v1:0", status="LEGACY"),
                        foundation("amazon.audio-v1:0", inputs=["AUDIO"]),
                        foundation("meta.other-v1:0", provider="Meta"),
                    ]
                },
                {},
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
                {},
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
                "list_foundation_models", "AccessDeniedException", expected_params={}
            )
            with self.assertRaises(ClientError):
                bedrock.discover(aws)

    def test_manifest_keeps_profile_id_and_requires_catalog_membership(self):
        model_id = "us.meta.test-v1:0"
        resource = bedrock.manifest(
            "us-west-2",
            "bedrock",
            "llm",
            ["team"],
            [model_id, model_id],
            [{"modelId": model_id}],
        )
        self.assertEqual(resource["spec"]["models"]["declared"], [model_id])
        self.assertEqual(resource["spec"]["access"], {"groups": ["team"]})
        self.assertNotIn("credentialSecretName", resource["spec"]["provider"])
        with self.assertRaisesRegex(ValueError, "absent"):
            bedrock.manifest("us-west-2", "bedrock", "llm", ["team"], ["unknown"], [])


class VerificationTests(unittest.TestCase):
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

    def test_gateway_checks_both_modes_and_denies_wrong_scope(self):
        requests = []

        def open_request(request, timeout):
            requests.append(request)
            body = json.loads(request.data)
            auth = request.headers.get("Authorization")
            status = {None: 401, "Bearer wrong-scope": 403, "Bearer permitted": 200}[
                auth
            ]
            response_body = b"{}"
            content_type = "application/json"
            if status == 200:
                if body["stream"]:
                    content_type = "text/event-stream"
                    response_body = b'data: {"choices":[{"delta":{"content":null},"finish_reason":null}]}\n\ndata: {"choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}\n\ndata: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'
                else:
                    response_body = b'{"choices":[{"message":{"content":"Hi"}}]}'
            response = io.BytesIO(response_body)
            response.status = status
            response.headers = {"Content-Type": content_type}
            return response

        opener = MagicMock()
        opener.open.side_effect = open_request
        result = bedrock.verify_gateway(
            "https://llm.example.com/v1", "model", "permitted", "wrong-scope", opener
        )
        self.assertEqual(result["wrongScope"], 403)
        self.assertEqual(len(requests), 6)
        self.assertEqual(
            [json.loads(r.data)["stream"] for r in requests], [False, True] * 3
        )

        def unauthenticated(*args, **kw):
            response = io.BytesIO(b"{}")
            response.status = 401
            return response

        opener.open.side_effect = unauthenticated
        with self.assertRaisesRegex(ValueError, "expected HTTP 403, got 401"):
            bedrock.verify_gateway(
                "https://llm.example.com", "model", "permitted", "wrong-scope", opener
            )

    def test_sse_rejects_truncated_or_error_responses(self):
        for body in [
            b"",
            b"data: [DONE]\n\n",
            b'data: {"error":{"message":"failed"}}\n\n',
        ]:
            with self.subTest(body=body), self.assertRaises(ValueError):
                bedrock.check_sse(io.BytesIO(body))

    def test_gateway_credentials_require_both_endpoints_before_aws_calls(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(
            bedrock.boto3, "Session"
        ) as session:
            with patch("sys.stderr", new=io.StringIO()):
                status = bedrock.main(
                    [
                        "--region",
                        "us-west-2",
                        "verify",
                        "--model-id",
                        "a",
                        "--external-url",
                        "https://llm.example.com",
                    ]
                )
            self.assertEqual(status, 1)
            session.assert_not_called()

    def test_rejects_gateway_urls_that_could_leak_credentials(self):
        for url in [
            "http://llm.example.com",
            "https://user:password@llm.example.com",
            "https://llm.example.com?token=1",
            "https://llm.example.com/other",
        ]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                bedrock.gateway_url(url)

    def test_live_command_reports_failure_and_continues_with_other_models(self):
        error = ClientError(
            {"Error": {"Code": "AccessDeniedException", "Message": "denied"}},
            "Converse",
        )
        with patch.object(bedrock.boto3, "Session"), patch.object(
            bedrock,
            "verify_aws",
            side_effect=[error, {"converse": USAGE, "converseStream": USAGE}],
        ), patch("sys.stdout", new=io.StringIO()) as output:
            status = bedrock.main(
                [
                    "--region",
                    "us-west-2",
                    "verify",
                    "--model-id",
                    "denied",
                    "--model-id",
                    "available",
                ]
            )
        self.assertEqual(status, 1)
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(rows[0]["failedStage"], "aws")
        self.assertEqual(rows[0]["error"], "AccessDeniedException")
        self.assertIn("aws", rows[1])
        self.assertTrue(all(row["gateway"] == "not tested" for row in rows))


if __name__ == "__main__":
    unittest.main()
