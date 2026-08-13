from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .enums import SessionViewMode


@dataclass(slots=True)
class PreferencesState:
    """Persisted product preferences, scoped independently per user."""

    session_view_by_user: dict[int, SessionViewMode] = field(default_factory=dict)

    def session_view(self, user_id: int) -> SessionViewMode:
        return self.session_view_by_user.get(user_id, SessionViewMode.HOST_FIRST)

    def set_session_view(self, user_id: int, mode: SessionViewMode) -> None:
        if mode is SessionViewMode.HOST_FIRST:
            self.session_view_by_user.pop(user_id, None)
            return
        self.session_view_by_user[user_id] = mode

    def to_dict(self) -> dict[str, Any]:
        return {
            "session_view_by_user": {
                str(user_id): mode.value
                for user_id, mode in self.session_view_by_user.items()
            }
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> PreferencesState:
        return cls(
            session_view_by_user={
                int(user_id): SessionViewMode(str(mode))
                for user_id, mode in data.get("session_view_by_user", {}).items()
            }
        )
