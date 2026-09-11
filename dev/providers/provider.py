#!/usr/bin/env python3
"""Discover provider models, generate manifests, and verify inference.

Discovery is read-only. Verification makes bounded, billable requests.
No command applies Kubernetes resources or changes cloud permissions.
Use --backend NAME --help for that backend's SDK options.
"""

import argparse
import json
import sys

from backends.bedrock import Bedrock
from base import Provider, ProviderError
from catalog import manifest
from gateway import gateway_credentials, verify_gateway

BACKENDS = {"bedrock": Bedrock}


def verify(provider: Provider, model_id, endpoints):
    result = {"modelId": model_id, "gateway": {} if endpoints else "not tested"}
    failed = False
    try:
        result["native"] = provider.verify_native(model_id)
    except (ProviderError, ValueError, OSError) as error:
        result["native"] = {"error": str(error)}
        failed = True
    # Gateway identity and caller identity are independent. Exercise both
    # endpoints even when a native check or the other endpoint fails.
    for kind, url, token, denied in endpoints:
        try:
            result["gateway"][kind] = verify_gateway(url, model_id, token, denied)
        except (ValueError, OSError) as error:
            result["gateway"][kind] = {"error": str(error)}
            failed = True
    return result, failed


def main(argv=None):
    selector = argparse.ArgumentParser(add_help=False)
    selector.add_argument("--backend", choices=BACKENDS)
    selection, _ = selector.parse_known_args(argv)

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backend", choices=BACKENDS, required=True)
    if selection.backend:
        BACKENDS[selection.backend].add_arguments(parser)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("discover", help="read the provider's current catalog")
    generate = commands.add_parser(
        "manifest", help="generate a resource for selected catalog IDs"
    )
    generate.add_argument("--model-id", action="append", required=True)
    generate.add_argument("--name", required=True)
    generate.add_argument("--namespace", default="nebari-llm-serving-system")
    generate.add_argument("--group", action="append", required=True)
    check = commands.add_parser(
        "verify", help="billable: native and optional gateway inference checks"
    )
    check.add_argument("--model-id", action="append", required=True)
    check.add_argument("--external-url")
    check.add_argument("--internal-url")
    args = parser.parse_args(argv)
    try:
        endpoints = gateway_credentials(args) if args.command == "verify" else []
        provider: Provider = BACKENDS[args.backend](args)
        if args.command in ("discover", "manifest"):
            catalog = provider.discover()
            result = {
                "backend": args.backend,
                "provider": provider.provider_spec(),
                "models": catalog,
            }
            if args.command == "manifest":
                result = manifest(
                    provider.provider_spec(),
                    args.name,
                    args.namespace,
                    args.group,
                    args.model_id,
                    catalog,
                )
            print(json.dumps(result, indent=2))
            return 0

        failed = False
        for model_id in dict.fromkeys(args.model_id):
            result, model_failed = verify(provider, model_id, endpoints)
            result["backend"] = args.backend
            failed |= model_failed
            print(json.dumps(result), flush=True)
        return int(failed)
    except (ProviderError, ValueError, OSError) as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
