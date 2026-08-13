from __future__ import annotations

import asyncio
import time

from ..config import Settings
from ..control.coordinator import Coordinator
from ..domain.enums import HostKind, HostStatus, SessionState
from ..domain.host import Host
from ..domain.session import Session
from ..persistence.json_store import JsonStateStore
from ..runtime.base import HostRuntime, RuntimeSession
from ..runtime.registry import RuntimeRegistry
from .event_stream import HubEventStream


class HubService:
    """Composition root for state and host-aware application services."""

    def __init__(
        self,
        settings: Settings,
        store: JsonStateStore,
        runtimes: RuntimeRegistry,
    ) -> None:
        self.settings = settings
        self.store = store
        self.runtimes = runtimes
        self.state = store.load()
        self.coordinator = Coordinator(self.state, runtimes, store)
        self.mutation_lock = asyncio.Lock()
        self.events = HubEventStream()

    @classmethod
    def load(cls, settings: Settings) -> HubService:
        store = JsonStateStore(settings.state_file, local_name=settings.host_name)
        return cls(settings, store, RuntimeRegistry())

    def connect_runtime(
        self,
        runtime: HostRuntime,
        *,
        name: str,
        home_dir: str = "",
        agent_version: str = "",
        kind: HostKind = HostKind.AGENT,
    ) -> Host:
        self.runtimes.replace(runtime)
        host = self.state.hosts.get(runtime.host_id)
        if host is None:
            host = Host(id=runtime.host_id, name=name, kind=kind)
            self.state.hosts[host.id] = host
        host.name = name
        host.home_dir = home_dir
        host.agent_version = agent_version
        host.enabled = True
        host.status = HostStatus.ONLINE
        host.last_seen_at = time.time()
        self.store.save(self.state)
        return host

    async def synchronize(self, host_id: str) -> Host:
        runtime = self.runtimes.get(host_id)
        host = self.state.hosts[host_id]
        health = await runtime.health()
        host.status = health.status
        host.runtime_version = health.version
        host.capabilities = set(health.capabilities)
        host.last_seen_at = time.time()
        if health.status is not HostStatus.ONLINE:
            self.store.save(self.state)
            return host
        self._merge_snapshot(host_id, await runtime.snapshot())
        self.store.save(self.state)
        return host

    def disconnect_runtime(self, host_id: str) -> None:
        self.runtimes.unregister(host_id)
        host = self.state.hosts.get(host_id)
        if host is not None:
            host.status = HostStatus.OFFLINE
            self.store.save(self.state)

    def _merge_snapshot(
        self, host_id: str, runtime_sessions: list[RuntimeSession]
    ) -> None:
        seen_session_ids = {remote.session_id for remote in runtime_sessions}
        now = time.time()
        for session in self.state.sessions.values():
            if (
                session.host_id == host_id
                and session.is_live
                and session.id not in seen_session_ids
            ):
                session.state = SessionState.LOST
                session.window_id = ""
                session.last_event_at = now
        for remote in runtime_sessions:
            session = self.state.sessions.get(remote.session_id)
            if session is None:
                session = Session.create(
                    session_id=remote.session_id,
                    name=remote.name,
                    host_id=host_id,
                    workdir=remote.workdir,
                    backend=remote.backend,
                )
                self.state.sessions[session.id] = session
            elif session.host_id != host_id:
                raise ValueError(
                    f"session ID collision across hosts: {remote.session_id}"
                )
            was_live = session.is_live
            session.name = remote.name
            session.window_id = remote.window_id
            session.workdir = remote.workdir
            session.backend = remote.backend
            session.provider_session_id = remote.provider_session_id
            session.state = remote.state
            if not was_live and session.is_live:
                session.restored_at = now
                session.live_since_at = now
            session.last_event_at = now
