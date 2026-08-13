from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .host import LOCAL_HOST_ID


@dataclass(slots=True)
class NavigationState:
    """Per-user host focus and the last active session on every host."""

    active_host_by_user: dict[int, str] = field(default_factory=dict)
    active_session_by_host: dict[int, dict[str, str]] = field(default_factory=dict)

    def selected_host(self, user_id: int) -> str:
        return self.active_host_by_user.get(user_id, LOCAL_HOST_ID)

    def selected_session_id(self, user_id: int, host_id: str | None = None) -> str:
        target_host = host_id or self.selected_host(user_id)
        return self.active_session_by_host.get(user_id, {}).get(target_host, "")

    def switch_host(self, user_id: int, host_id: str) -> str:
        self.active_host_by_user[user_id] = host_id
        return self.selected_session_id(user_id, host_id)

    def activate_session(self, user_id: int, host_id: str, session_id: str) -> None:
        self.active_host_by_user[user_id] = host_id
        self.active_session_by_host.setdefault(user_id, {})[host_id] = session_id

    def clear_session(self, session_id: str) -> None:
        for per_host in self.active_session_by_host.values():
            for host_id, active_id in list(per_host.items()):
                if active_id == session_id:
                    del per_host[host_id]

    def to_dict(self) -> dict[str, Any]:
        return {
            "active_host_by_user": {
                str(user_id): host_id
                for user_id, host_id in self.active_host_by_user.items()
            },
            "active_session_by_host": {
                str(user_id): dict(per_host)
                for user_id, per_host in self.active_session_by_host.items()
            },
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> NavigationState:
        return cls(
            active_host_by_user={
                int(user_id): str(host_id)
                for user_id, host_id in data.get("active_host_by_user", {}).items()
            },
            active_session_by_host={
                int(user_id): {
                    str(host_id): str(session_id)
                    for host_id, session_id in per_host.items()
                }
                for user_id, per_host in data.get(
                    "active_session_by_host", {}
                ).items()
            },
        )

