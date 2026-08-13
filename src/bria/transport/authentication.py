from __future__ import annotations

import secrets
from collections.abc import Callable, Mapping

type TokenValidator = Callable[[str, str], bool]


class StaticTokenValidator:
    """Constant-time validator for deployments backed by a token mapping."""

    def __init__(self, tokens: Mapping[str, str]) -> None:
        self._tokens = dict(tokens)

    def __call__(self, host_id: str, token: str) -> bool:
        expected = self._tokens.get(host_id)
        return expected is not None and secrets.compare_digest(expected, token)
def bearer_token(authorization: str | None) -> str | None:
    if authorization is None:
        return None
    scheme, separator, credentials = authorization.partition(" ")
    if separator and scheme.lower() == "bearer" and credentials:
        return credentials
    return None
