from __future__ import annotations

import json
import time
import uuid
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

from .version import PROTOCOL_VERSION


class ProtocolError(ValueError):
    pass


class MessageKind(StrEnum):
    HELLO = "hello"
    SNAPSHOT = "snapshot"
    COMMAND = "command"
    RESULT = "result"
    EVENT = "event"
    ACK = "ack"
    HEARTBEAT = "heartbeat"
    ERROR = "error"


@dataclass(frozen=True, slots=True)
class Envelope:
    kind: MessageKind
    host_id: str
    payload: dict[str, Any] = field(default_factory=dict)
    request_id: str = ""
    sequence: int = 0
    sent_at: float = field(default_factory=time.time)
    version: int = PROTOCOL_VERSION

    @classmethod
    def new_request(
        cls, kind: MessageKind, host_id: str, payload: dict[str, Any]
    ) -> Envelope:
        return cls(
            kind=kind,
            host_id=host_id,
            payload=payload,
            request_id=uuid.uuid4().hex,
        )

    def to_json(self) -> str:
        return json.dumps(
            {
                "version": self.version,
                "kind": self.kind.value,
                "host_id": self.host_id,
                "request_id": self.request_id,
                "sequence": self.sequence,
                "sent_at": self.sent_at,
                "payload": self.payload,
            },
            separators=(",", ":"),
            sort_keys=True,
        )

    @classmethod
    def from_json(cls, raw: str) -> Envelope:
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ProtocolError("invalid JSON envelope") from exc
        if not isinstance(data, dict):
            raise ProtocolError("envelope must be an object")
        version = int(data.get("version", 0))
        if version != PROTOCOL_VERSION:
            raise ProtocolError(
                f"unsupported protocol version: {version}; expected {PROTOCOL_VERSION}"
            )
        host_id = str(data.get("host_id", "")).strip()
        if not host_id:
            raise ProtocolError("host_id is required")
        payload = data.get("payload", {})
        if not isinstance(payload, dict):
            raise ProtocolError("payload must be an object")
        try:
            kind = MessageKind(str(data["kind"]))
        except (KeyError, ValueError) as exc:
            raise ProtocolError("unknown or missing message kind") from exc
        return cls(
            version=version,
            kind=kind,
            host_id=host_id,
            request_id=str(data.get("request_id", "")),
            sequence=int(data.get("sequence", 0)),
            sent_at=float(data.get("sent_at", 0.0)),
            payload=payload,
        )

