"""Stable domain import surface; implementations stay in focused modules."""

from .enums import Capability, HostKind, HostStatus, SessionState, SessionViewMode
from .host import LOCAL_HOST_ID, Host
from .navigation import NavigationState
from .preferences import PreferencesState
from .project_state import STATE_SCHEMA_VERSION, ProjectState
from .session import Session

__all__ = [
    "Capability",
    "Host",
    "HostKind",
    "HostStatus",
    "LOCAL_HOST_ID",
    "NavigationState",
    "ProjectState",
    "PreferencesState",
    "STATE_SCHEMA_VERSION",
    "Session",
    "SessionState",
    "SessionViewMode",
]
