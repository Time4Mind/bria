from __future__ import annotations

import json
import os
import tempfile
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

from ..runtime.transcript_source import TranscriptCursor

_SCHEMA_VERSION = 1


class LocalMonitorStateError(RuntimeError):
    pass


@dataclass(slots=True)
class SessionMonitorState:
    runtime_signature: str = ""
    binding_signature: str = ""
    transcript_cursor: TranscriptCursor = TranscriptCursor()

    def to_dict(self) -> dict[str, Any]:
        result = asdict(self)
        result["transcript_cursor"] = asdict(self.transcript_cursor)
        return result

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> SessionMonitorState:
        cursor = data.get("transcript_cursor", {})
        if not isinstance(cursor, dict):
            raise ValueError("transcript_cursor must be an object")
        return cls(
            runtime_signature=str(data.get("runtime_signature", "")),
            binding_signature=str(data.get("binding_signature", "")),
            transcript_cursor=TranscriptCursor(
                offset=int(cursor.get("offset", 0)),
                file_id=str(cursor.get("file_id", "")),
            ),
        )


class LocalMonitorStateStore:
    """Durable transcript cursors and emitted runtime signatures."""

    def __init__(self, path: Path) -> None:
        self.path = path
        self.sessions = self._load()

    def get(self, session_id: str) -> SessionMonitorState:
        return self.sessions.setdefault(session_id, SessionMonitorState())

    def save(self) -> None:
        payload = {
            "schema_version": _SCHEMA_VERSION,
            "sessions": {
                key: value.to_dict() for key, value in self.sessions.items()
            },
        }
        try:
            encoded = json.dumps(payload, separators=(",", ":"), sort_keys=True)
            self.path.parent.mkdir(parents=True, exist_ok=True)
            fd, temporary_name = tempfile.mkstemp(
                dir=self.path.parent, prefix=f".{self.path.name}.", suffix=".tmp"
            )
            temporary = Path(temporary_name)
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as handle:
                    handle.write(encoded + "\n")
                    handle.flush()
                    os.fsync(handle.fileno())
                temporary.chmod(0o600)
                temporary.replace(self.path)
            finally:
                temporary.unlink(missing_ok=True)
        except (OSError, TypeError, ValueError) as exc:
            raise LocalMonitorStateError(
                f"cannot persist local event monitor state: {self.path}"
            ) from exc

    def _load(self) -> dict[str, SessionMonitorState]:
        if not self.path.exists():
            return {}
        try:
            raw = json.loads(self.path.read_text(encoding="utf-8"))
            if not isinstance(raw, dict):
                raise ValueError("monitor state must be an object")
            if int(raw.get("schema_version", 0)) != _SCHEMA_VERSION:
                raise ValueError("unsupported monitor state schema")
            sessions = raw.get("sessions", {})
            if not isinstance(sessions, dict):
                raise ValueError("monitor sessions must be an object")
            return {
                str(key): SessionMonitorState.from_dict(value)
                for key, value in sessions.items()
                if isinstance(value, dict)
            }
        except (OSError, TypeError, ValueError, json.JSONDecodeError) as exc:
            raise LocalMonitorStateError(
                f"cannot load local event monitor state: {self.path}"
            ) from exc
