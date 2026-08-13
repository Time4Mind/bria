from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import secrets
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

_HOST_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")


class InvalidHostIdError(ValueError):
    pass


@dataclass(frozen=True, slots=True)
class IssuedToken:
    host_id: str
    token: str
    issued_at: float


@dataclass(frozen=True, slots=True)
class TokenRecord:
    digest: str
    issued_at: float

    def to_dict(self) -> dict[str, object]:
        return {"digest": self.digest, "issued_at": self.issued_at}

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> TokenRecord:
        return cls(
            digest=str(data["digest"]),
            issued_at=float(data.get("issued_at", 0.0)),
        )


class TokenRegistry:
    """Hub-owned, hashed credentials for outbound host agents."""

    def __init__(self, path: Path) -> None:
        self.path = path
        self._records = self._load()

    def issue(self, host_id: str) -> IssuedToken:
        normalized = _validate_host_id(host_id)
        token = secrets.token_urlsafe(32)
        issued_at = time.time()
        self._records[normalized] = TokenRecord(
            digest=_digest(token), issued_at=issued_at
        )
        self._save()
        return IssuedToken(normalized, token, issued_at)

    def verify(self, host_id: str, token: str) -> bool:
        try:
            normalized = _validate_host_id(host_id)
        except InvalidHostIdError:
            return False
        record = self._records.get(normalized)
        if record is None or not token:
            return False
        return hmac.compare_digest(record.digest, _digest(token))

    def revoke(self, host_id: str) -> bool:
        normalized = _validate_host_id(host_id)
        removed = self._records.pop(normalized, None) is not None
        if removed:
            self._save()
        return removed

    def known_host_ids(self) -> tuple[str, ...]:
        return tuple(sorted(self._records))

    def _load(self) -> dict[str, TokenRecord]:
        if not self.path.exists():
            return {}
        data = json.loads(self.path.read_text(encoding="utf-8"))
        if not isinstance(data, dict):
            raise ValueError("agent token registry must be an object")
        return {
            _validate_host_id(str(host_id)): TokenRecord.from_dict(record)
            for host_id, record in data.items()
            if isinstance(record, dict)
        }

    def _save(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        payload = {
            host_id: record.to_dict()
            for host_id, record in sorted(self._records.items())
        }
        encoded = json.dumps(payload, indent=2, sort_keys=True) + "\n"
        fd, temp_name = tempfile.mkstemp(
            prefix=f".{self.path.name}.", dir=self.path.parent, text=True
        )
        temp_path = Path(temp_name)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write(encoded)
                handle.flush()
                os.fsync(handle.fileno())
            os.chmod(temp_path, 0o600)
            os.replace(temp_path, self.path)
        finally:
            temp_path.unlink(missing_ok=True)


def _validate_host_id(host_id: str) -> str:
    normalized = host_id.strip()
    if not _HOST_ID_PATTERN.fullmatch(normalized):
        raise InvalidHostIdError(
            "host_id must be 1-64 letters, digits, dots, underscores, or hyphens"
        )
    return normalized


def _digest(token: str) -> str:
    return hashlib.sha256(token.encode("utf-8")).hexdigest()
