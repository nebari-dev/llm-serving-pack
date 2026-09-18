"""AWS-native catalog and Converse checks, using Boto3 and Botocore."""

from contextlib import contextmanager

import boto3
from botocore.config import Config
from botocore.exceptions import BotoCoreError, ClientError
from botocore.utils import ArnParser

from base import MAX_TOKENS, PROMPT, ProviderError

MESSAGES = [{"role": "user", "content": [{"text": PROMPT}]}]


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
    # Use AWS's filters and paginator instead of maintaining a provider/model
    # registry or constructing profile IDs locally.
    summaries = []
    for publisher in providers or [None]:
        filters = {"byOutputModality": "TEXT"}
        if publisher:
            filters["byProvider"] = publisher
        summaries.extend(client.list_foundation_models(**filters)["modelSummaries"])
    foundations = {model["modelId"]: model for model in summaries if text_model(model)}
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
                ArnParser()
                .parse_arn(model["modelArn"])["resource"]
                .removeprefix("foundation-model/")
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


@contextmanager
def aws_errors():
    try:
        yield
    except ClientError as error:
        raise ProviderError(error.response["Error"]["Code"]) from error
    except BotoCoreError as error:
        raise ProviderError(type(error).__name__) from error


class Bedrock:
    @staticmethod
    def add_arguments(parser):
        parser.add_argument("--region", required=True)
        parser.add_argument(
            "--profile", help="AWS profile; otherwise use the SDK credential chain"
        )
        parser.add_argument(
            "--publisher",
            action="append",
            default=[],
            help="AWS model provider name, repeatable",
        )

    def __init__(self, args):
        self.region = args.region
        self.publishers = args.publisher
        with aws_errors():
            self.session = boto3.Session(
                profile_name=args.profile, region_name=args.region
            )
        self.config = Config(
            connect_timeout=5, read_timeout=60, retries={"total_max_attempts": 1}
        )

    def provider_spec(self):
        return {
            "backend": {"type": "Bedrock", "bedrock": {"region": self.region}},
            "credential": {"type": "WorkloadIdentity"},
        }

    def discover(self):
        with aws_errors():
            return discover(
                self.session.client("bedrock", config=self.config), self.publishers
            )

    def verify_native(self, model_id):
        with aws_errors():
            return verify_aws(
                self.session.client("bedrock-runtime", config=self.config), model_id
            )
