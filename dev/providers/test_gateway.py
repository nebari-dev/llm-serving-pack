"""Shared HTTP, streaming, and authorization checks for every backend."""

import io
import json
import unittest
from unittest.mock import MagicMock

import gateway


class GatewayTests(unittest.TestCase):
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
        result = gateway.verify_gateway(
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
            gateway.verify_gateway(
                "https://llm.example.com", "model", "permitted", "wrong-scope", opener
            )

    def test_sse_rejects_truncated_or_error_responses(self):
        for body in [
            b"",
            b"data: [DONE]\n\n",
            b'data: {"error":{"message":"failed"}}\n\n',
        ]:
            with self.subTest(body=body), self.assertRaises(ValueError):
                gateway.check_sse(io.BytesIO(body))

    def test_bedrock_terminal_marker_with_one_newline(self):
        # Bedrock's translator emits complete SSE events, then a [DONE] line
        # with only one newline. This matches the live v0.5.0 gateway output.
        text = b'data: {"choices":[{"delta":{"content":"Hello!"}}]}\n\n'
        finish = b'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n'
        for ending in (b"\n", b"\r\n", b"\n\n"):
            with self.subTest(ending=ending):
                gateway.check_sse(io.BytesIO(text + finish + b"data: [DONE]" + ending))
        for body in (
            text + finish,
            text + finish + b"data: [DONE]",
            text + finish + b"data: [DON\n",
            text + b"data: [DONE]\n",
            finish + b"data: [DONE]\n",
            text + finish + b'data: {"choices":[]}',
        ):
            with self.subTest(body=body), self.assertRaises(ValueError):
                gateway.check_sse(io.BytesIO(body))

    def test_rejects_gateway_urls_that_could_leak_credentials(self):
        for url in [
            "http://llm.example.com",
            "https://user:password@llm.example.com",
            "https://llm.example.com?token=1",
            "https://llm.example.com/other",
        ]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                gateway.gateway_url(url)
