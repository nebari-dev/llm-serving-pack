#!/usr/bin/env python3
"""Discover Bedrock targets and verify Converse using the AWS SDK.

Discovery is read-only. Verification makes bounded, billable inference requests.
No command applies Kubernetes resources or changes AWS permissions.
"""

import argparse
import json
import os
import sys
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

import boto3
from botocore.config import Config
from botocore.exceptions import BotoCoreError, ClientError

MESSAGES = [{"role": "user", "content": [{"text": "Reply with one short greeting."}]}]
MAX_TOKENS = 32


def text_model(model):
    return (
        model.get("modelLifecycle", {}).get("status") == "ACTIVE"
        and "TEXT" in model.get("inputModalities", [])
        and "TEXT" in model.get("outputModalities", [])
    )


def discover(client, providers=()):
    """Return current text targets; catalog membership is not Converse support.

    Inference profile IDs come from AWS, never from adding a region prefix.
    Profiles with missing or inactive foundation models are not advertised.
    """
    foundations = {
        model["modelId"]: model
        for model in client.list_foundation_models()["modelSummaries"]
        if text_model(model) and (not providers or model["providerName"] in providers)
    }
    targets = {}

    def add(model_id, kind, model_ids):
        targets[model_id] = {
            "modelId": model_id,
            "kind": kind,
            "foundationModelIds": sorted(model_ids),
            "providers": sorted({foundations[m]["providerName"] for m in model_ids}),
            "streamingAdvertised": all(
                foundations[m].get("responseStreamingSupported", False)
                for m in model_ids
            ),
            "converse": "unverified",
        }

    for model_id, model in foundations.items():
        if "ON_DEMAND" in model.get("inferenceTypesSupported", []):
            add(model_id, "foundation-model", {model_id})

    for page in client.get_paginator("list_inference_profiles").paginate():
        for profile in page["inferenceProfileSummaries"]:
            model_ids = {
                model["modelArn"].split("foundation-model/", 1)[-1]
                for model in profile["models"]
            }
            if (
                profile["status"] == "ACTIVE"
                and model_ids
                and model_ids <= foundations.keys()
            ):
                # Application profiles are account-scoped; keep their ARN intact.
                model_id = (
                    profile["inferenceProfileArn"]
                    if profile["type"] == "APPLICATION"
                    else profile["inferenceProfileId"]
                )
                add(model_id, "inference-profile", model_ids)

    # Availability is useful evidence, but does not establish InvokeModel IAM
    # access, destination-region access for a profile, or Converse compatibility.
    availability = {}
    for model_id in sorted(
        {m for t in targets.values() for m in t["foundationModelIds"]}
    ):
        try:
            result = client.get_foundation_model_availability(modelId=model_id)
            availability[model_id] = {
                k: v
                for k, v in result.items()
                if k not in ("ResponseMetadata", "modelId")
            }
        except ClientError as error:
            availability[model_id] = {"error": error.response["Error"]["Code"]}
    for target in targets.values():
        target["availability"] = {
            m: availability[m] for m in target["foundationModelIds"]
        }
    return [targets[m] for m in sorted(targets)]


def manifest(region, name, namespace, groups, model_ids, catalog):
    known = {m["modelId"] for m in catalog}
    missing = set(model_ids) - known
    if missing:
        raise ValueError(
            "targets absent from the active text catalog: " + ", ".join(sorted(missing))
        )
    return {
        "apiVersion": "llm.nebari.dev/v1alpha1",
        "kind": "PassthroughModel",
        "metadata": {"name": name, "namespace": namespace},
        "spec": {
            "provider": {
                "backend": {"type": "Bedrock", "bedrock": {"region": region}},
                "credential": {"type": "WorkloadIdentity"},
            },
            "models": {"declared": sorted(set(model_ids))},
            "access": {"groups": groups},
        },
    }


def verify_aws(runtime, model_id):
    """Use the same native API that the gateway's AWSBedrock translator uses."""
    request = {
        "modelId": model_id,
        "messages": MESSAGES,
        "inferenceConfig": {"maxTokens": MAX_TOKENS},
    }
    result = runtime.converse(**request)
    content = result.get("output", {}).get("message", {}).get("content", [])
    if not any(block.get("text", "").strip() for block in content) or not result.get(
        "stopReason"
    ):
        raise ValueError("Converse did not return assistant text and a stop reason")
    if result.get("usage", {}).get("outputTokens", 0) <= 0:
        raise ValueError("Converse did not report output token usage")

    response = runtime.converse_stream(**request)
    stream = response["stream"]
    text, stopped, usage = False, False, None
    try:
        for event in stream:
            if any(key.endswith("Exception") for key in event):
                raise ValueError("ConverseStream returned " + ", ".join(event))
            text |= bool(
                event.get("contentBlockDelta", {})
                .get("delta", {})
                .get("text", "")
                .strip()
            )
            stopped |= bool(event.get("messageStop", {}).get("stopReason"))
            if "metadata" in event:
                usage = event["metadata"].get("usage")
    finally:
        stream.close()
    if not text or not stopped or not usage or usage.get("outputTokens", 0) <= 0:
        raise ValueError(
            "ConverseStream ended without text, stop reason, or token usage"
        )
    return {"converse": result["usage"], "converseStream": usage}


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


def verify_gateway(url, model_id, token, denied_token, opener=None):
    opener = opener or build_opener(NoRedirects())
    url = gateway_url(url)
    for credential, expected in [(None, 401), (denied_token, 403), (token, 200)]:
        for streaming in [False, True]:
            body = {
                "model": model_id,
                "messages": [
                    {"role": "user", "content": MESSAGES[0]["content"][0]["text"]}
                ],
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
    return {
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
            "BEDROCK_TEST_API_KEY",
            "BEDROCK_TEST_DENIED_API_KEY",
        ),
        ("internal", args.internal_url, "BEDROCK_TEST_JWT", "BEDROCK_TEST_DENIED_JWT"),
    ]:
        gateway_url(url)
        if not os.environ.get(good) or not os.environ.get(denied):
            raise ValueError(f"set {good} and {denied} to run gateway checks")
        endpoints.append((kind, url, os.environ[good], os.environ[denied]))
    return endpoints


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--region", required=True)
    parser.add_argument(
        "--profile", help="AWS profile; otherwise use the SDK default credential chain"
    )
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("discover", "manifest"):
        command = commands.add_parser(name)
        command.add_argument(
            "--provider",
            action="append",
            default=[],
            help="AWS provider name, repeatable",
        )
        if name == "manifest":
            command.add_argument("--model-id", action="append", required=True)
            command.add_argument("--name", default="bedrock")
            command.add_argument("--namespace", default="nebari-llm-serving-system")
            command.add_argument("--group", action="append", required=True)
    verify = commands.add_parser(
        "verify", help="billable: 2 AWS requests plus 4 gateway requests per model"
    )
    verify.add_argument("--model-id", action="append", required=True)
    verify.add_argument("--external-url")
    verify.add_argument("--internal-url")
    args = parser.parse_args(argv)
    try:
        endpoints = gateway_credentials(args) if args.command == "verify" else []
        session = boto3.Session(profile_name=args.profile, region_name=args.region)
        config = Config(
            connect_timeout=5, read_timeout=60, retries={"total_max_attempts": 1}
        )
        if args.command in ("discover", "manifest"):
            catalog = discover(session.client("bedrock", config=config), args.provider)
            result = {"region": args.region, "models": catalog}
            if args.command == "manifest":
                result = manifest(
                    args.region,
                    args.name,
                    args.namespace,
                    args.group,
                    args.model_id,
                    catalog,
                )
            print(json.dumps(result, indent=2))
            return 0

        runtime = session.client("bedrock-runtime", config=config)
        failed = False
        for model_id in dict.fromkeys(args.model_id):
            result = {
                "modelId": model_id,
                "region": args.region,
                "gateway": "not tested",
            }
            stage = "aws"
            try:
                result["aws"] = verify_aws(runtime, model_id)
                if endpoints:
                    result["gateway"] = {}
                    for kind, url, token, denied in endpoints:
                        stage = kind
                        result["gateway"][kind] = verify_gateway(
                            url, model_id, token, denied
                        )
            except (BotoCoreError, ClientError, ValueError, OSError) as error:
                result["failedStage"] = stage
                result["error"] = (
                    error.response["Error"]["Code"]
                    if isinstance(error, ClientError)
                    else type(error).__name__ + ": " + str(error)
                )
                failed = True
            print(json.dumps(result), flush=True)
        return int(failed)
    except (BotoCoreError, ClientError, ValueError) as error:
        # Do not print response bodies or credentials.
        message = (
            error.response["Error"]["Code"]
            if isinstance(error, ClientError)
            else str(error)
        )
        print(message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
