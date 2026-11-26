"""API keys created through the key-manager REST API.

Only the ApiKey type lives here so far; the release state file (release.py)
carries one. The client verbs arrive with the key journeys.
"""

from dataclasses import dataclass, field


@dataclass(frozen=True)
class ApiKey:
    namespace: str
    model: str
    client_id: str
    secret: str = field(repr=False)  # never in a repr, a log or a failure message
