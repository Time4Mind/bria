from __future__ import annotations

import time
from collections.abc import Callable
from dataclasses import dataclass

from ..domain.enums import ARCHIVED_SESSION_STATES, SessionState, SessionViewMode
from ..domain.host import Host
from ..domain.project_state import ProjectState
from ..domain.session import Session
from ..persistence.base import StateStore
from ..runtime.base import CaptureResult, CreateSessionRequest, DirectoryEntry
from ..runtime.registry import RuntimeRegistry
from .archive_listing import ArchivedSessionListItem, ArchiveListing
from .host_listing import HostListing, HostListItem
from .session_listing import SessionListing, SessionListItem


class NoActiveSessionError(LookupError):
    pass


@dataclass(frozen=True, slots=True)
class HostSelection:
    host: Host
    session: Session | None


class Coordinator:
    """Host-aware application service; adapters call this instead of runtimes."""

    def __init__(
        self,
        state: ProjectState,
        runtimes: RuntimeRegistry,
        store: StateStore | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.state = state
        self.runtimes = runtimes
        self.store = store
        self.clock = clock

    def switch_host(self, user_id: int, host_id: str) -> HostSelection:
        host = self._require_host(host_id)
        selected_id = self.state.navigation.switch_host(user_id, host_id)
        session = self.state.sessions.get(selected_id) if selected_id else None
        if session is None or not session.is_live or session.host_id != host_id:
            session = self._latest_live_session(host_id)
            if session is not None:
                self.state.navigation.activate_session(user_id, host_id, session.id)
        self._save()
        return HostSelection(host=host, session=session)

    def host_listing(self, user_id: int) -> HostListing:
        selected_host_id = self.state.navigation.selected_host(user_id)
        hosts = sorted(
            self.state.hosts.values(),
            key=lambda item: (item.name.casefold(), item.id),
        )
        return HostListing(
            tuple(
                HostListItem(
                    host=host,
                    live_session_count=len(self.state.live_sessions(host.id)),
                    is_selected=host.id == selected_host_id,
                )
                for host in hosts
                if host.enabled
            )
        )

    def session_view_mode(self, user_id: int) -> SessionViewMode:
        return self.state.preferences.session_view(user_id)

    def known_host_ids(self) -> tuple[str, ...]:
        return tuple(self.state.hosts)

    def known_session_ids(self) -> tuple[str, ...]:
        return tuple(self.state.sessions)

    def activate_session(self, user_id: int, session_id: str) -> Session:
        session = self._require_session(session_id)
        if not session.is_live:
            raise ValueError(f"session is not live: {session_id}")
        self.state.navigation.activate_session(user_id, session.host_id, session.id)
        self._save()
        return session

    def selected_session(self, user_id: int) -> Session | None:
        session_id = self.state.navigation.selected_session_id(user_id)
        session = self.state.sessions.get(session_id)
        if session is None or not session.is_live:
            return None
        return session

    def sessions_for_selected_host(self, user_id: int) -> list[Session]:
        host_id = self.state.navigation.selected_host(user_id)
        return self.state.live_sessions(host_id)

    def set_session_view_mode(
        self, user_id: int, mode: SessionViewMode
    ) -> SessionListing:
        self.state.preferences.set_session_view(user_id, mode)
        self._save()
        return self.session_listing(user_id)

    def session_listing(self, user_id: int) -> SessionListing:
        mode = self.state.preferences.session_view(user_id)
        selected_host_id = self.state.navigation.selected_host(user_id)
        selected_host = self._require_host(selected_host_id)
        selected_session_id = self.state.navigation.selected_session_id(user_id)
        if mode is SessionViewMode.HOST_FIRST:
            sessions = self.state.live_sessions(selected_host_id)
        else:
            sessions = self.state.live_sessions()
        items = tuple(
            SessionListItem(
                session=session,
                host=self._require_host(session.host_id),
                is_selected=session.id == selected_session_id,
            )
            for session in sessions
        )
        return SessionListing(
            mode=mode,
            selected_host=selected_host,
            items=items,
        )

    def archive_listing(self, user_id: int) -> ArchiveListing:
        mode = self.state.preferences.session_view(user_id)
        selected_host_id = self.state.navigation.selected_host(user_id)
        selected_host = self._require_host(selected_host_id)
        host_filter = selected_host_id if mode is SessionViewMode.HOST_FIRST else None
        items = tuple(
            ArchivedSessionListItem(
                session=session,
                host=self._require_host(session.host_id),
            )
            for session in self.state.archived_sessions(host_filter)
        )
        return ArchiveListing(mode=mode, selected_host=selected_host, items=items)

    async def create_session(
        self,
        user_id: int,
        *,
        workdir: str,
        name: str,
        backend: str = "claude",
        resume_provider_session_id: str = "",
    ) -> Session:
        host_id = self.state.navigation.selected_host(user_id)
        self._require_host(host_id)
        runtime = self.runtimes.get(host_id)
        session = Session.create(
            name=name,
            host_id=host_id,
            workdir=workdir,
            backend=backend,
        )
        result = await runtime.create_session(
            CreateSessionRequest(
                session_id=session.id,
                workdir=workdir,
                name=name,
                backend=backend,
                resume_provider_session_id=resume_provider_session_id,
            )
        )
        session.window_id = result.window_id
        session.provider_session_id = result.provider_session_id
        session.state = result.state
        self.state.sessions[session.id] = session
        self.state.navigation.activate_session(user_id, host_id, session.id)
        self._save()
        return session

    async def send_text(self, user_id: int, text: str) -> Session:
        session = self._require_selected_session(user_id)
        await self.send_text_to(session.id, text)
        return session

    async def send_text_to(self, session_id: str, text: str) -> Session:
        session = self._require_session(session_id)
        if not session.is_live:
            raise ValueError(f"session is not live: {session_id}")
        await self.runtimes.get(session.host_id).send_text(session.id, text)
        return session

    async def send_key(self, user_id: int, key: str) -> Session:
        session = self._require_selected_session(user_id)
        await self.runtimes.get(session.host_id).send_key(session.id, key)
        return session

    async def capture_selected(self, user_id: int) -> CaptureResult:
        session = self._require_selected_session(user_id)
        return await self.capture_session(session.id)

    async def capture_session(self, session_id: str) -> CaptureResult:
        session = self._require_session(session_id)
        if not session.is_live:
            raise ValueError(f"session is not live: {session_id}")
        return await self.runtimes.get(session.host_id).capture_pane(session.id)

    def session_details(self, session_id: str) -> tuple[Session, Host]:
        session = self._require_session(session_id)
        return session, self._require_host(session.host_id)

    async def list_directories(self, user_id: int, path: str) -> list[DirectoryEntry]:
        host_id = self.state.navigation.selected_host(user_id)
        self._require_host(host_id)
        return await self.runtimes.get(host_id).list_directories(path)

    async def upload_file(self, user_id: int, name: str, content: bytes) -> str:
        session = self._require_selected_session(user_id)
        return await self.runtimes.get(session.host_id).upload_file(
            session.id, name, content
        )

    async def archive_session(self, user_id: int, session_id: str) -> None:
        session = self._require_session(session_id)
        if not session.is_live:
            raise ValueError(f"session is not live: {session_id}")
        await self.runtimes.get(session.host_id).archive_session(session.id)
        session.state = SessionState.ARCHIVED
        session.window_id = ""
        session.archived_at = self.clock()
        self.state.navigation.clear_session(session.id)
        if self.state.navigation.selected_host(user_id) == session.host_id:
            fallback = self._latest_live_session(session.host_id)
            if fallback is not None:
                self.state.navigation.activate_session(
                    user_id, session.host_id, fallback.id
                )
        self._save()

    async def restore_session(self, user_id: int, session_id: str) -> Session:
        session = self._require_session(session_id)
        if session.state not in ARCHIVED_SESSION_STATES:
            raise ValueError(f"session cannot be restored: {session_id}")

        result = await self.runtimes.get(session.host_id).restore_session(session.id)
        if result.session_id != session.id:
            raise ValueError(
                f"runtime restored unexpected session: {result.session_id}"
            )
        if result.state not in (SessionState.ACTIVE, SessionState.IDLE):
            raise ValueError(f"runtime did not restore a live session: {session_id}")

        restored_at = self.clock()
        session.window_id = result.window_id
        session.provider_session_id = result.provider_session_id
        session.state = result.state
        session.restored_at = restored_at
        session.live_since_at = restored_at
        session.last_event_at = restored_at
        self.state.navigation.activate_session(user_id, session.host_id, session.id)
        self._save()
        return session

    def _latest_live_session(self, host_id: str) -> Session | None:
        sessions = self.state.live_sessions(host_id)
        return max(
            sessions,
            key=lambda item: (item.last_event_at, item.id),
            default=None,
        )

    def _require_host(self, host_id: str) -> Host:
        try:
            return self.state.hosts[host_id]
        except KeyError as exc:
            raise LookupError(f"unknown host: {host_id}") from exc

    def _require_session(self, session_id: str) -> Session:
        try:
            return self.state.sessions[session_id]
        except KeyError as exc:
            raise LookupError(f"unknown session: {session_id}") from exc

    def _require_selected_session(self, user_id: int) -> Session:
        session = self.selected_session(user_id)
        if session is None:
            raise NoActiveSessionError("no active session on the selected host")
        return session

    def _save(self) -> None:
        if self.store is not None:
            self.store.save(self.state)
