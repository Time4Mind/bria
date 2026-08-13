from __future__ import annotations

from enum import StrEnum


class HostKind(StrEnum):
    LOCAL = "local"
    AGENT = "agent"


class HostStatus(StrEnum):
    ONLINE = "online"
    RECONNECTING = "reconnecting"
    OFFLINE = "offline"
    DISABLED = "disabled"


class SessionState(StrEnum):
    ACTIVE = "active"
    IDLE = "idle"
    ARCHIVED = "archived"
    COMPLETED = "completed"
    LOST = "lost"


class SessionViewMode(StrEnum):
    HOST_FIRST = "host_first"
    ALL_HOSTS = "all_hosts"


class Capability(StrEnum):
    SESSIONS = "sessions"
    DIRECTORIES = "directories"
    TRANSCRIPTS = "transcripts"
    PANE_CAPTURE = "pane_capture"
    INTERACTIVE_KEYS = "interactive_keys"
    FILE_UPLOAD = "file_upload"
    AUTH = "auth"
    USAGE = "usage"


LIVE_SESSION_STATES = frozenset({SessionState.ACTIVE, SessionState.IDLE})
ARCHIVED_SESSION_STATES = frozenset(
    {SessionState.ARCHIVED, SessionState.COMPLETED, SessionState.LOST}
)
