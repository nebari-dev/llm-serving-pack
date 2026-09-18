"""The small interface implemented by each provider's SDK adapter."""

import argparse
from typing import Protocol

PROMPT = "Reply with one short greeting."
MAX_TOKENS = 32


class ProviderError(Exception):
    """A provider failure safe to report without credentials or response bodies."""


class Provider(Protocol):
    @staticmethod
    def add_arguments(parser: argparse.ArgumentParser) -> None:
        """Register backend-specific CLI options; called on the class before parsing."""
        ...

    def __init__(self, args: argparse.Namespace) -> None:
        """Build the adapter from parsed arguments, deferring network calls."""
        ...

    def provider_spec(self) -> dict:
        """Return the PassthroughModel provider settings, never credential values."""
        ...

    def discover(self) -> list[dict]:
        """Return current targets with modelId plus provider-specific metadata."""
        ...

    def verify_native(self, model_id: str) -> dict:
        """Check native completion and streaming with the provider's SDK."""
        ...
