from __future__ import annotations

import pytest

from bria.control.coordinator import Coordinator
from bria.domain.enums import SessionState, SessionViewMode
from bria.domain.host import Host
from bria.domain.project_state import ProjectState
from bria.domain.session import Session
from bria.runtime.base import RuntimeSession
from bria.runtime.memory import MemoryRuntime
from bria.runtime.registry import RuntimeRegistry


class FailingArchiveRuntime(MemoryRuntime):
    async def archive_session(self, session_id: str) -> None:
        raise RuntimeError(f"archive failed: {session_id}")


class FailingRestoreRuntime(MemoryRuntime):
    async def restore_session(self, session_id: str) -> RuntimeSession:
        raise RuntimeError(f"restore failed: {session_id}")


def make_session(
    session_id: str,
    host_id: str,
    *,
    state: SessionState = SessionState.ACTIVE,
    created_at: float = 1.0,
    live_since_at: float = 0.0,
    archived_at: float = 0.0,
    last_event_at: float = 0.0,
) -> Session:
    return Session(
        id=session_id,
        name=session_id,
        host_id=host_id,
        workdir=f"/srv/{session_id}",
        state=state,
        created_at=created_at,
        live_since_at=live_since_at or created_at,
        archived_at=archived_at,
        last_event_at=last_event_at,
    )


def attach_runtime_session(runtime: MemoryRuntime, session: Session) -> None:
    runtime.sessions[session.id] = RuntimeSession(
        session_id=session.id,
        window_id=session.window_id,
        workdir=session.workdir,
        name=session.name,
        backend=session.backend,
        provider_session_id=session.provider_session_id,
        state=session.state,
    )


@pytest.mark.asyncio
async def test_restore_preserves_creation_and_moves_session_to_live_tail() -> None:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    old = make_session(
        "old",
        "server-a",
        state=SessionState.ARCHIVED,
        created_at=10.0,
        archived_at=30.0,
    )
    current = make_session("current", "server-a", created_at=20.0)
    state.sessions = {old.id: old, current.id: current}
    runtime = MemoryRuntime("server-a")
    attach_runtime_session(runtime, old)
    coordinator = Coordinator(
        state,
        RuntimeRegistry([runtime]),
        clock=lambda: 40.0,
    )

    restored = await coordinator.restore_session(7, old.id)

    assert restored.created_at == 10.0
    assert restored.archived_at == 30.0
    assert restored.restored_at == 40.0
    assert restored.live_since_at == 40.0
    assert restored.last_event_at == 40.0
    assert restored.state is SessionState.ACTIVE
    assert [item.id for item in state.live_sessions("server-a")] == [
        current.id,
        old.id,
    ]
    assert state.navigation.selected_host(7) == "server-a"
    assert state.navigation.selected_session_id(7) == old.id


@pytest.mark.asyncio
async def test_restore_uses_original_host_and_updates_runtime_handles() -> None:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    state.hosts["server-b"] = Host(id="server-b", name="Server B")
    archived = make_session("archive", "server-a", state=SessionState.ARCHIVED)
    state.sessions[archived.id] = archived
    runtime_a = MemoryRuntime("server-a")
    runtime_b = MemoryRuntime("server-b")
    attach_runtime_session(runtime_a, archived)
    runtime_a.sessions[archived.id].provider_session_id = "provider-restored"
    coordinator = Coordinator(
        state,
        RuntimeRegistry([runtime_a, runtime_b]),
        clock=lambda: 50.0,
    )
    coordinator.switch_host(3, "server-b")

    restored = await coordinator.restore_session(3, archived.id)

    assert restored.window_id
    assert restored.provider_session_id == "provider-restored"
    assert state.navigation.selected_host(3) == "server-a"
    assert archived.id not in runtime_b.sessions


@pytest.mark.asyncio
async def test_archive_sets_timestamp_and_fallback_stays_on_same_host() -> None:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    state.hosts["server-b"] = Host(id="server-b", name="Server B")
    fallback = make_session("fallback", "server-a", last_event_at=20.0)
    selected = make_session("selected", "server-a", last_event_at=30.0)
    other_host = make_session("other", "server-b", last_event_at=100.0)
    state.sessions = {
        fallback.id: fallback,
        selected.id: selected,
        other_host.id: other_host,
    }
    runtime_a = MemoryRuntime("server-a")
    for session in (fallback, selected):
        attach_runtime_session(runtime_a, session)
    coordinator = Coordinator(
        state,
        RuntimeRegistry([runtime_a, MemoryRuntime("server-b")]),
        clock=lambda: 75.0,
    )
    coordinator.activate_session(4, selected.id)

    await coordinator.archive_session(4, selected.id)

    assert selected.state is SessionState.ARCHIVED
    assert selected.archived_at == 75.0
    assert selected.window_id == ""
    assert coordinator.selected_session(4) is fallback


@pytest.mark.asyncio
@pytest.mark.parametrize("operation", ["archive", "restore"])
async def test_runtime_failure_does_not_mutate_hub_state(operation: str) -> None:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    session_state = (
        SessionState.ACTIVE if operation == "archive" else SessionState.ARCHIVED
    )
    session = make_session("target", "server-a", state=session_state)
    state.sessions[session.id] = session
    runtime: MemoryRuntime
    if operation == "archive":
        runtime = FailingArchiveRuntime("server-a")
    else:
        runtime = FailingRestoreRuntime("server-a")
    attach_runtime_session(runtime, session)
    coordinator = Coordinator(state, RuntimeRegistry([runtime]), clock=lambda: 90.0)
    coordinator.switch_host(8, "server-a")
    if session.is_live:
        coordinator.activate_session(8, session.id)
    before = state.to_dict()

    with pytest.raises(RuntimeError, match=f"{operation} failed"):
        if operation == "archive":
            await coordinator.archive_session(8, session.id)
        else:
            await coordinator.restore_session(8, session.id)

    assert state.to_dict() == before


@pytest.mark.asyncio
async def test_restore_rejects_live_session_before_runtime_call() -> None:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    session = make_session("live", "server-a")
    state.sessions[session.id] = session
    runtime = MemoryRuntime("server-a")
    attach_runtime_session(runtime, session)
    coordinator = Coordinator(state, RuntimeRegistry([runtime]))
    before = state.to_dict()

    with pytest.raises(ValueError, match="cannot be restored"):
        await coordinator.restore_session(1, session.id)

    assert state.to_dict() == before
    assert runtime.sessions[session.id].state is SessionState.ACTIVE


def test_archive_listing_obeys_view_mode_and_newest_first_order() -> None:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    state.hosts["server-b"] = Host(id="server-b", name="Server B")
    older = make_session(
        "older",
        "server-a",
        state=SessionState.ARCHIVED,
        archived_at=10.0,
    )
    fallback_timestamp = make_session(
        "fallback",
        "server-a",
        state=SessionState.LOST,
        last_event_at=20.0,
    )
    newest = make_session(
        "newest",
        "server-b",
        state=SessionState.COMPLETED,
        archived_at=30.0,
    )
    state.sessions = {
        older.id: older,
        fallback_timestamp.id: fallback_timestamp,
        newest.id: newest,
    }
    coordinator = Coordinator(state, RuntimeRegistry())
    coordinator.switch_host(12, "server-a")

    host_first = coordinator.archive_listing(12)
    global_listing = coordinator.set_session_view_mode(12, SessionViewMode.ALL_HOSTS)
    del global_listing  # The setting also controls the dedicated archive listing.

    assert [item.session.id for item in host_first.items] == ["fallback", "older"]
    all_hosts = coordinator.archive_listing(12)
    assert [item.session.id for item in all_hosts.items] == [
        "newest",
        "fallback",
        "older",
    ]
    assert all_hosts.items[0].qualified_name == "newest · Server B"


@pytest.mark.parametrize("schema_version", [1, 2])
def test_older_schema_migrates_live_order_timestamps(schema_version: int) -> None:
    state = ProjectState.from_dict(
        {
            "schema_version": schema_version,
            "sessions": {
                "legacy": {
                    "id": "legacy",
                    "name": "Legacy",
                    "created_at": 123.0,
                    "state": "active",
                }
            },
        }
    )

    assert state.schema_version == 3
    assert state.sessions["legacy"].created_at == 123.0
    assert state.sessions["legacy"].live_since_at == 123.0
    assert state.sessions["legacy"].restored_at == 0.0
