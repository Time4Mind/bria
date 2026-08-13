from __future__ import annotations

import base64
import hashlib
import re
from collections.abc import Iterable

_CALLBACK = re.compile(r"^[a-z][a-z0-9_-]{0,15}(?::[A-Za-z0-9_-]{1,24})?$")


def callback(action: str, argument: str = "") -> str:
    value = f"{action}:{argument}" if argument else action
    if len(value.encode()) > 64 or _CALLBACK.fullmatch(value) is None:
        raise ValueError("invalid Telegram callback data")
    return value


def parse(value: str) -> tuple[str, str]:
    if len(value.encode()) > 64 or _CALLBACK.fullmatch(value) is None:
        raise ValueError("invalid Telegram callback data")
    action, separator, argument = value.partition(":")
    return action, argument if separator else ""


def entity_token(kind: str, identifier: str) -> str:
    digest = hashlib.blake2s(
        f"{kind}\0{identifier}".encode(), digest_size=9
    ).digest()
    return base64.urlsafe_b64encode(digest).decode().rstrip("=")


def resolve_token(kind: str, token: str, identifiers: Iterable[str]) -> str:
    matches = [
        identifier
        for identifier in identifiers
        if entity_token(kind, identifier) == token
    ]
    if len(matches) != 1:
        raise LookupError("callback target is stale or ambiguous")
    return matches[0]
