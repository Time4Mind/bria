from __future__ import annotations

from ..domain.enums import Capability, HostStatus, SessionState
from .base import (
    CaptureResult,
    CreateSessionRequest,
    DirectoryEntry,
    HostHealth,
    RuntimeSession,
)


class MemoryRuntime:
    """Deterministic runtime for contract tests and UI prototyping."""

    def __init__(self, host_id: str) -> None:
        self._host_id = host_id
        self.sessions: dict[str, RuntimeSession] = {}
        self.sent_text: list[tuple[str, str]] = []
        self.sent_keys: list[tuple[str, str]] = []
        self.files: dict[tuple[str, str], bytes] = {}

    @property
    def host_id(self) -> str:
        return self._host_id

    async def health(self) -> HostHealth:
        return HostHealth(
            status=HostStatus.ONLINE,
            version="memory",
            capabilities=frozenset(Capability),
        )

    async def snapshot(self) -> list[RuntimeSession]:
        return list(self.sessions.values())

    async def list_directories(self, path: str) -> list[DirectoryEntry]:
        return [DirectoryEntry(name="project", path=f"{path.rstrip('/')}/project")]

    async def create_session(self, request: CreateSessionRequest) -> RuntimeSession:
        session = RuntimeSession(
            session_id=request.session_id,
            window_id=f"@{len(self.sessions) + 1}",
            workdir=request.workdir,
            name=request.name,
            backend=request.backend,
            provider_session_id=request.resume_provider_session_id,
        )
        self.sessions[session.session_id] = session
        return session

    async def send_text(self, session_id: str, text: str) -> None:
        self._require_session(session_id)
        self.sent_text.append((session_id, text))

    async def send_key(self, session_id: str, key: str) -> None:
        self._require_session(session_id)
        self.sent_keys.append((session_id, key))

    async def capture_pane(self, session_id: str) -> CaptureResult:
        session = self._require_session(session_id)
        return CaptureResult(text=f"{self.host_id}:{session.name}")

    async def archive_session(self, session_id: str) -> None:
        session = self._require_session(session_id)
        session.state = SessionState.ARCHIVED
        session.window_id = ""

    async def restore_session(self, session_id: str) -> RuntimeSession:
        session = self._require_session(session_id)
        session.state = SessionState.ACTIVE
        session.window_id = f"@{len(self.sessions) + 1}"
        return session

    async def upload_file(self, session_id: str, name: str, content: bytes) -> str:
        self._require_session(session_id)
        self.files[(session_id, name)] = content
        return f".ccbot-inbox/{name}"

    def _require_session(self, session_id: str) -> RuntimeSession:
        try:
            return self.sessions[session_id]
        except KeyError as exc:
            raise LookupError(f"unknown runtime session: {session_id}") from exc

