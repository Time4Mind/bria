from __future__ import annotations

import secrets
import time
from dataclasses import dataclass
from typing import Any

from .enums import LIVE_SESSION_STATES, SessionState
from .host import LOCAL_HOST_ID


@dataclass(slots=True)
class Session:
    """Global session metadata; execution handles are scoped by ``host_id``."""

    id: str
    name: str
    host_id: str = LOCAL_HOST_ID
    window_id: str = ""
    workdir: str = ""
    backend: str = "claude"
    provider_session_id: str = ""
    state: SessionState = SessionState.ACTIVE
    created_at: float = 0.0
    live_since_at: float = 0.0
    last_event_at: float = 0.0
    archived_at: float = 0.0
    restored_at: float = 0.0

    @classmethod
    def create(
        cls,
        *,
        name: str,
        host_id: str,
        workdir: str,
        backend: str = "claude",
        session_id: str | None = None,
    ) -> Session:
        now = time.time()
        return cls(
            id=session_id or secrets.token_hex(4),
            name=name,
            host_id=host_id,
            workdir=workdir,
            backend=backend,
            created_at=now,
            live_since_at=now,
            last_event_at=now,
        )

    @property
    def is_live(self) -> bool:
        return self.state in LIVE_SESSION_STATES

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "name": self.name,
            "host_id": self.host_id,
            "window_id": self.window_id,
            "workdir": self.workdir,
            "backend": self.backend,
            "provider_session_id": self.provider_session_id,
            "state": self.state.value,
            "created_at": self.created_at,
            "live_since_at": self.live_since_at,
            "last_event_at": self.last_event_at,
            "archived_at": self.archived_at,
            "restored_at": self.restored_at,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Session:
        created_at = float(data.get("created_at", 0.0))
        return cls(
            id=str(data["id"]),
            name=str(data.get("name") or data["id"]),
            host_id=str(data.get("host_id", LOCAL_HOST_ID)),
            window_id=str(data.get("window_id", "")),
            workdir=str(data.get("workdir", "")),
            backend=str(data.get("backend", "claude")),
            provider_session_id=str(
                data.get("provider_session_id") or data.get("claude_session_id") or ""
            ),
            state=SessionState(str(data.get("state", SessionState.ACTIVE.value))),
            created_at=created_at,
            live_since_at=float(data.get("live_since_at", created_at)),
            last_event_at=float(data.get("last_event_at", 0.0)),
            archived_at=float(data.get("archived_at", 0.0)),
            restored_at=float(data.get("restored_at", 0.0)),
        )
