"""API keys created through the key-manager REST API.

KeyManagerClient speaks the routes key-manager/internal/api/handler.go
registers, with the shapes it writes:

  GET    /api/me                                   {"username", "name", "email", "groups"}
  GET    /api/models                               {"models": [ModelInfo]}  (PascalCase: no JSON tags)
  GET    /api/keys                                 {"keys": [KeyInfo]}      (camelCase)
  POST   /api/keys {"modelName", "description"}    201 {"clientId", "apiKey"}
  DELETE /api/keys/{namespace}/{model}/{clientID}  204

ApiKey.secret never appears in a repr, a log or a failure message.
"""

from collections.abc import Callable
from dataclasses import dataclass, field
from urllib.parse import quote

import requests

from llm_journeys.target import Endpoint

REQUEST_TIMEOUT = 30
OK, CREATED, NO_CONTENT = 200, 201, 204


@dataclass(frozen=True)
class ApiKey:
    namespace: str
    model: str
    client_id: str
    secret: str = field(repr=False)  # never in a repr, a log or a failure message


class KeyManagerError(RuntimeError):
    """The key manager answered with an unexpected status."""


class KeyManagerClient:
    def __init__(self, endpoint: Endpoint, token: Callable[[], str]) -> None:
        self._endpoint = endpoint
        self._token = token  # asked at each request (Q50)

    def _request(self, method: str, path: str, want: int, json: dict | None = None) -> requests.Response:
        response = requests.request(
            method,
            f"{self._endpoint.base_url}{path}",
            headers={"Authorization": f"Bearer {self._token()}"},
            json=json,
            verify=self._endpoint.verify,
            timeout=REQUEST_TIMEOUT,
        )
        if response.status_code != want:
            message = f"{method} {path} returned HTTP {response.status_code}, want {want}"
            # A success body may carry a key; error bodies are the handler's plain-text http.Error messages.
            if not 200 <= response.status_code < 300:
                message += f": {response.text.strip()[:200]}"
            raise KeyManagerError(message)
        return response

    def me(self) -> dict:
        return self._request("GET", "/api/me", OK).json()

    def models(self) -> list[dict]:
        return self._request("GET", "/api/models", OK).json()["models"] or []

    def list_keys(self) -> list[dict]:
        return self._request("GET", "/api/keys", OK).json()["keys"] or []

    def create_key(self, model_name: str, description: str = "") -> ApiKey:
        created = self._request(
            "POST", "/api/keys", CREATED, json={"modelName": model_name, "description": description}
        ).json()
        client_id = created["clientId"]
        # The POST answer has no namespace; the key's listing entry carries it.
        for key in self.list_keys():
            if key["clientId"] == client_id and key["modelName"] == model_name:
                return ApiKey(
                    namespace=key["namespace"], model=model_name, client_id=client_id, secret=created["apiKey"]
                )
        raise KeyManagerError(f"key {client_id} for model {model_name} was created but GET /api/keys does not list it")

    def revoke_key(self, key: ApiKey) -> None:
        path = "/api/keys/" + "/".join(quote(part, safe="") for part in (key.namespace, key.model, key.client_id))
        self._request("DELETE", path, NO_CONTENT)
