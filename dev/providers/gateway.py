"""Provider-independent checks for the pack's two inference endpoints."""

import json
import os
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

from base import MAX_TOKENS, PROMPT


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Do not forward gateway credentials to a redirected host.
        return None


def gateway_url(value):
    url = urlsplit(value)
    if (
        url.scheme != "https"
        or not url.hostname
        or url.username
        or url.password
        or url.query
        or url.fragment
    ):
        raise ValueError(
            "gateway URLs must be HTTPS base URLs without credentials, query, or fragment"
        )
    if url.path.rstrip("/") not in ("", "/v1"):
        raise ValueError("gateway URL path must be empty or /v1")
    return value.rstrip("/").removesuffix("/v1") + "/v1/chat/completions"


def check_sse(response):
    text, finished, done = False, False, False
    data = []
    line_complete = False
    for raw in response:
        line_complete = raw.endswith(b"\n")
        line = raw.decode("utf-8").rstrip("\r\n")
        if line.startswith("data:"):
            data.append(line[5:].lstrip())
        elif not line and data:
            payload = "\n".join(data)
            data = []
            if payload == "[DONE]":
                done = True
                break
            chunk = json.loads(payload)
            if "error" in chunk:
                raise ValueError("gateway returned a streaming error")
            for choice in chunk.get("choices", []):
                text |= bool((choice.get("delta", {}).get("content") or "").strip())
                finished |= bool(choice.get("finish_reason"))
    # Envoy AI Gateway ends Bedrock streams with "data: [DONE]\n", without
    # an extra blank line. Accept that terminal marker at EOF, but not an
    # unterminated line or a pending JSON event from a truncated response.
    done |= line_complete and data == ["[DONE]"]
    if not (text and finished and done):
        raise ValueError("gateway stream ended without text, finish reason, or [DONE]")


def verify_catalog(url, model_id, token, opener=None):
    opener = opener or build_opener(NoRedirects())
    url = gateway_url(url).removesuffix("/chat/completions") + "/models"
    request = Request(url, headers={"Authorization": "Bearer " + token})
    with opener.open(request, timeout=60) as response:
        if response.status != 200:
            raise ValueError(f"gateway catalog returned HTTP {response.status}")
        result = json.load(response)
    if model_id not in {model.get("id") for model in result.get("data", [])}:
        raise ValueError("selected model is absent from the gateway catalog")


def verify_gateway(url, model_id, token, denied_token, opener=None):
    opener = opener or build_opener(NoRedirects())
    base_url = url
    url = gateway_url(url)
    for credential, expected in [(None, 401), (denied_token, 403), (token, 200)]:
        for streaming in [False, True]:
            body = {
                "model": model_id,
                "messages": [{"role": "user", "content": PROMPT}],
                "max_tokens": MAX_TOKENS,
                "stream": streaming,
            }
            headers = {"Content-Type": "application/json"}
            if credential:
                headers["Authorization"] = "Bearer " + credential
            request = Request(
                url, data=json.dumps(body).encode(), headers=headers, method="POST"
            )
            try:
                response = opener.open(request, timeout=60)
            except HTTPError as error:
                response = error
            with response:
                if response.status != expected:
                    raise ValueError(
                        f"gateway expected HTTP {expected}, got {response.status} (stream={streaming})"
                    )
                if expected != 200:
                    continue
                if streaming:
                    if "text/event-stream" not in response.headers.get(
                        "Content-Type", ""
                    ):
                        raise ValueError("gateway did not return an SSE stream")
                    check_sse(response)
                else:
                    result = json.load(response)
                    choices = result.get("choices", [])
                    if (
                        not choices
                        or not (
                            choices[0].get("message", {}).get("content") or ""
                        ).strip()
                    ):
                        raise ValueError("gateway did not return assistant text")
    verify_catalog(base_url, model_id, token, opener)
    return {
        "catalog": "verified",
        "completion": "verified",
        "stream": "verified",
        "unauthenticated": 401,
        "wrongScope": 403,
    }


def gateway_credentials(args):
    if bool(args.external_url) != bool(args.internal_url):
        raise ValueError("provide both --external-url and --internal-url")
    if not args.external_url:
        return []
    endpoints = []
    for kind, url, good, denied in [
        (
            "external",
            args.external_url,
            "PROVIDER_TEST_API_KEY",
            "PROVIDER_TEST_DENIED_API_KEY",
        ),
        (
            "internal",
            args.internal_url,
            "PROVIDER_TEST_JWT",
            "PROVIDER_TEST_DENIED_JWT",
        ),
    ]:
        gateway_url(url)
        if not os.environ.get(good) or not os.environ.get(denied):
            raise ValueError(f"set {good} and {denied} to run gateway checks")
        endpoints.append((kind, url, os.environ[good], os.environ[denied]))
    return endpoints
