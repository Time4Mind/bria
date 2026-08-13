from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .enums import ARCHIVED_SESSION_STATES, LIVE_SESSION_STATES
from .host import LOCAL_HOST_ID, Host
from .navigation import NavigationState
from .preferences import PreferencesState
from .session import Session

STATE_SCHEMA_VERSION = 3
SUPPORTED_STATE_SCHEMA_VERSIONS = frozenset({1, 2, STATE_SCHEMA_VERSION})


@dataclass(slots=True)
class ProjectState:
    schema_version: int = STATE_SCHEMA_VERSION
    hosts: dict[str, Host] = field(default_factory=dict)
    sessions: dict[str, Session] = field(default_factory=dict)
    navigation: NavigationState = field(default_factory=NavigationState)
    preferences: PreferencesState = field(default_factory=PreferencesState)

    @classmethod
    def empty(cls, local_name: str | None = None) -> ProjectState:
        local = Host.local(local_name)
        return cls(hosts={local.id: local})

    def ensure_local_host(self, name: str | None = None) -> Host:
        host = self.hosts.get(LOCAL_HOST_ID)
        if host is None:
            host = Host.local(name)
            self.hosts[host.id] = host
        return host

    def sessions_for_host(
        self, host_id: str, *, live_only: bool = False
    ) -> list[Session]:
        sessions = [item for item in self.sessions.values() if item.host_id == host_id]
        if live_only:
            sessions = [item for item in sessions if item.state in LIVE_SESSION_STATES]
        return sorted(sessions, key=self._live_sort_key)

    def live_sessions(self, host_id: str | None = None) -> list[Session]:
        sessions = [
            session
            for session in self.sessions.values()
            if session.state in LIVE_SESSION_STATES
            and (host_id is None or session.host_id == host_id)
        ]
        return sorted(sessions, key=self._live_sort_key)

    def archived_sessions(self, host_id: str | None = None) -> list[Session]:
        sessions = [
            session
            for session in self.sessions.values()
            if session.state in ARCHIVED_SESSION_STATES
            and (host_id is None or session.host_id == host_id)
        ]
        return sorted(sessions, key=self._archive_sort_key)

    @staticmethod
    def _live_sort_key(session: Session) -> tuple[float, str]:
        return (session.live_since_at or session.created_at, session.id)

    @staticmethod
    def _archive_sort_key(session: Session) -> tuple[float, str]:
        timestamp = session.archived_at or session.last_event_at or session.created_at
        return (-timestamp, session.id)

    def to_dict(self) -> dict[str, Any]:
        return {
            "schema_version": self.schema_version,
            "hosts": {host_id: host.to_dict() for host_id, host in self.hosts.items()},
            "sessions": {
                session_id: session.to_dict()
                for session_id, session in self.sessions.items()
            },
            "navigation": self.navigation.to_dict(),
            "preferences": self.preferences.to_dict(),
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> ProjectState:
        version = int(data.get("schema_version", STATE_SCHEMA_VERSION))
        if version not in SUPPORTED_STATE_SCHEMA_VERSIONS:
            raise ValueError(f"unsupported state schema version: {version}")
        state = cls(
            schema_version=STATE_SCHEMA_VERSION,
            hosts={
                str(host_id): Host.from_dict(host_data)
                for host_id, host_data in data.get("hosts", {}).items()
            },
            sessions={
                str(session_id): Session.from_dict(session_data)
                for session_id, session_data in data.get("sessions", {}).items()
            },
            navigation=NavigationState.from_dict(data.get("navigation", {})),
            preferences=PreferencesState.from_dict(data.get("preferences", {})),
        )
        state.ensure_local_host()
        return state

    @classmethod
    def from_legacy_ccbot(
        cls, data: dict[str, Any], *, local_name: str | None = None
    ) -> ProjectState:
        """Import the current ccbot state without modifying the source file."""
        state = cls.empty(local_name)
        state.sessions = {
            str(session_id): Session.from_dict(session_data)
            for session_id, session_data in data.get("sessions", {}).items()
        }
        for session in state.sessions.values():
            session.host_id = LOCAL_HOST_ID
        for raw_user_id, session_id in data.get("active_sessions", {}).items():
            user_id = int(raw_user_id)
            if session_id in state.sessions:
                state.navigation.activate_session(
                    user_id, LOCAL_HOST_ID, str(session_id)
                )
        return state
