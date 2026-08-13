from __future__ import annotations

from dataclasses import dataclass, field
from typing import Protocol, runtime_checkable

from ..domain.enums import Capability, HostStatus, SessionState


@dataclass(frozen=True, slots=True)
class HostHealth:
    status: HostStatus
    detail: str = ""
    version: str = ""
    capabilities: frozenset[Capability] = frozenset()


@dataclass(frozen=True, slots=True)
class DirectoryEntry:
    name: str
    path: str
    modified_at: float = 0.0


@dataclass(frozen=True, slots=True)
class CreateSessionRequest:
    session_id: str
    workdir: str
    name: str
    backend: str = "claude"
    resume_provider_session_id: str = ""


@dataclass(slots=True)
class RuntimeSession:
    session_id: str
    window_id: str
    workdir: str
    name: str
    backend: str = "claude"
    provider_session_id: str = ""
    state: SessionState = SessionState.ACTIVE


@dataclass(frozen=True, slots=True)
class CaptureResult:
    text: str
    ansi: bool = True
    metadata: dict[str, str] = field(default_factory=dict)


@runtime_checkable
class HostRuntime(Protocol):
    """Host-local operations. Telegram and global navigation do not belong here."""

    @property
    def host_id(self) -> str: ...

    async def health(self) -> HostHealth: ...

    async def snapshot(self) -> list[RuntimeSession]: ...

    async def list_directories(self, path: str) -> list[DirectoryEntry]: ...

    async def create_session(self, request: CreateSessionRequest) -> RuntimeSession: ...

    async def send_text(self, session_id: str, text: str) -> None: ...

    async def send_key(self, session_id: str, key: str) -> None: ...

    async def capture_pane(self, session_id: str) -> CaptureResult: ...

    async def archive_session(self, session_id: str) -> None: ...

    async def restore_session(self, session_id: str) -> RuntimeSession: ...

    async def upload_file(
        self, session_id: str, name: str, content: bytes
    ) -> str: ...

