"""The small interface implemented by each provider's SDK adapter."""

from typing import Protocol

PROMPT = "Reply with one short greeting."
MAX_TOKENS = 32


class ProviderError(Exception):
    """A provider failure safe to report without credentials or response bodies."""


class Provider(Protocol):
    def provider_spec(self) -> dict:
        """Return the PassthroughModel provider settings, never credential values."""
        ...

    def discover(self) -> list[dict]:
        """Return current targets with modelId plus provider-specific metadata."""
        ...

    def verify_native(self, model_id: str) -> dict:
        """Check native completion and streaming with the provider's SDK."""
        ...
