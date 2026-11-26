"""User tokens for the internal endpoint and the key-manager API.

A TokenSource hands out a token valid for at least MIN_SECONDS_LEFT and
re-mints when the cached one is closer to expiry than that (Q50). On kind-dev
tokens come from a password grant; on nebari from a browser login
(BrowserLogin, journey tier).

A password never appears in a message, a repr or a log.
"""

import base64
import binascii
import json
import time
from collections.abc import Callable
from typing import Protocol

import requests

from llm_journeys.target import REALM_TOKEN_PATH, Endpoint, Target

MIN_SECONDS_LEFT = 60
REQUEST_TIMEOUT = 30


class TokenError(RuntimeError):
    """A token could not be minted; the message names the user and cause, never the password."""


class TokenSource(Protocol):
    def token(self, username: str, password: str) -> str: ...  # valid for at least 60 s; re-mints when needed (Q50)


def seconds_left(token: str, now: float) -> float:
    """Seconds until the token's exp claim, negative once expired."""
    try:
        payload = token.split(".")[1]
        claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
        return float(claims["exp"]) - now
    except (IndexError, binascii.Error, ValueError, KeyError, TypeError) as error:
        raise TokenError(f"token has no readable exp claim: {type(error).__name__}") from None


class PasswordGrant:
    """TokenSource: an OAuth2 password grant at the realm's token endpoint, cached per user."""

    def __init__(self, token_endpoint: Endpoint, client_id: str, *, clock: Callable[[], float] = time.time) -> None:
        self._url = f"{token_endpoint.base_url}{REALM_TOKEN_PATH}"
        self._verify = token_endpoint.verify
        self._client_id = client_id
        self._clock = clock
        self._cache: dict[tuple[str, str], str] = {}

    def token(self, username: str, password: str) -> str:
        cached = self._cache.get((username, password))
        if cached is not None and seconds_left(cached, self._clock()) >= MIN_SECONDS_LEFT:
            return cached
        token = self._mint(username, lambda: password)
        self._cache[(username, password)] = token
        return token

    def _mint(self, username: str, password: Callable[[], str]) -> str:
        # The password comes from a closure, never a parameter: a traceback
        # of a raise here prints this frame's arguments (C1).
        where = f"password grant for {username} at {self._url}"
        try:
            response = requests.post(
                self._url,
                data={
                    "client_id": self._client_id,
                    "username": username,
                    "password": password(),
                    "grant_type": "password",
                },
                verify=self._verify,
                timeout=REQUEST_TIMEOUT,
            )
        except requests.RequestException as error:
            # from None: the chained traceback's transport frames carry the form body.
            raise TokenError(f"{where} failed: {type(error).__name__}: {error}") from None
        try:
            body = response.json()
        except ValueError:
            body = None
        if response.status_code != 200:
            error = body.get("error") if isinstance(body, dict) else None
            raise TokenError(f"{where} returned HTTP {response.status_code}" + (f" ({error})" if error else ""))
        token = body.get("access_token") if isinstance(body, dict) else None
        if not isinstance(token, str) or not token:
            raise TokenError(f"{where} returned HTTP 200 without an access_token")
        return token


def token_source(target: Target, new_context: Callable[[], object]) -> TokenSource:
    """PasswordGrant where the target names a password-grant client, else BrowserLogin (journey tier)."""
    if target.password_grant_client is not None:
        return PasswordGrant(target.token_endpoint, target.password_grant_client)
    raise NotImplementedError(f"target {target.name}: tokens by browser login are not built yet")
