from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

from ..domain.enums import Capability


class CommandName(StrEnum):
    HEALTH = "health"
    SNAPSHOT = "snapshot"
    LIST_DIRECTORIES = "list_directories"
    CREATE_SESSION = "create_session"
    SEND_TEXT = "send_text"
    SEND_KEY = "send_key"
    CAPTURE_PANE = "capture_pane"
    ARCHIVE_SESSION = "archive_session"
    RESTORE_SESSION = "restore_session"
    UPLOAD_FILE = "upload_file"
    GET_USAGE = "get_usage"
    START_LOGIN = "start_login"


class EventName(StrEnum):
    HOST_STATUS = "host_status"
    SESSION_ANNOUNCED = "session_announced"
    SESSION_CHANGED = "session_changed"
    SESSION_OUTPUT = "session_output"
    INTERACTIVE_PROMPT = "interactive_prompt"
    AUTH_REQUIRED = "auth_required"
    USAGE_CHANGED = "usage_changed"


@dataclass(frozen=True, slots=True)
class Hello:
    agent_version: str
    protocol_version: int
    host_name: str
    home_dir: str
    capabilities: frozenset[Capability] = frozenset()
    last_acked_sequence: int = 0

    def to_payload(self) -> dict[str, Any]:
        return {
            "agent_version": self.agent_version,
            "protocol_version": self.protocol_version,
            "host_name": self.host_name,
            "home_dir": self.home_dir,
            "capabilities": sorted(item.value for item in self.capabilities),
            "last_acked_sequence": self.last_acked_sequence,
        }

    @classmethod
    def from_payload(cls, payload: dict[str, Any]) -> Hello:
        return cls(
            agent_version=str(payload.get("agent_version", "")),
            protocol_version=int(payload.get("protocol_version", 0)),
            host_name=str(payload.get("host_name", "")),
            home_dir=str(payload.get("home_dir", "")),
            capabilities=frozenset(
                Capability(str(value)) for value in payload.get("capabilities", [])
            ),
            last_acked_sequence=int(payload.get("last_acked_sequence", 0)),
        )


@dataclass(frozen=True, slots=True)
class Command:
    name: CommandName
    arguments: dict[str, Any] = field(default_factory=dict)
    idempotency_key: str = ""

    def to_payload(self) -> dict[str, Any]:
        return {
            "name": self.name.value,
            "arguments": self.arguments,
            "idempotency_key": self.idempotency_key,
        }

    @classmethod
    def from_payload(cls, payload: dict[str, Any]) -> Command:
        arguments = payload.get("arguments", {})
        if not isinstance(arguments, dict):
            raise ValueError("command arguments must be an object")
        return cls(
            name=CommandName(str(payload["name"])),
            arguments=arguments,
            idempotency_key=str(payload.get("idempotency_key", "")),
        )


@dataclass(frozen=True, slots=True)
class Event:
    name: EventName
    session_id: str = ""
    data: dict[str, Any] = field(default_factory=dict)

    def to_payload(self) -> dict[str, Any]:
        return {
            "name": self.name.value,
            "session_id": self.session_id,
            "data": self.data,
        }

    @classmethod
    def from_payload(cls, payload: dict[str, Any]) -> Event:
        data = payload.get("data", {})
        if not isinstance(data, dict):
            raise ValueError("event data must be an object")
        return cls(
            name=EventName(str(payload["name"])),
            session_id=str(payload.get("session_id", "")),
            data=data,
        )
