from __future__ import annotations

import pytest

from bria.control.coordinator import Coordinator
from bria.domain.enums import SessionViewMode
from bria.domain.host import Host
from bria.domain.project_state import ProjectState
from bria.runtime.memory import MemoryRuntime
from bria.runtime.registry import RuntimeRegistry


@pytest.mark.asyncio
async def test_routes_input_to_selected_host_and_restores_last_session() -> None:
    state = ProjectState.empty("local")
    state.hosts["server-a"] = Host(id="server-a", name="Build server")
    state.hosts["server-b"] = Host(id="server-b", name="GPU server")
    runtime_a = MemoryRuntime("server-a")
    runtime_b = MemoryRuntime("server-b")
    coordinator = Coordinator(state, RuntimeRegistry([runtime_a, runtime_b]))

    coordinator.switch_host(1, "server-a")
    session_a = await coordinator.create_session(
        1, workdir="/srv/a", name="alpha"
    )
    coordinator.switch_host(1, "server-b")
    session_b = await coordinator.create_session(
        1, workdir="/srv/b", name="beta", backend="codex"
    )
    await coordinator.send_text(1, "on beta")

    selection = coordinator.switch_host(1, "server-a")
    await coordinator.send_text(1, "on alpha")

    assert selection.session == session_a
    assert coordinator.selected_session(1) == session_a
    assert runtime_a.sent_text == [(session_a.id, "on alpha")]
    assert runtime_b.sent_text == [(session_b.id, "on beta")]


@pytest.mark.asyncio
async def test_archiving_selected_session_activates_host_local_fallback() -> None:
    state = ProjectState.empty("local")
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    runtime = MemoryRuntime("server-a")
    coordinator = Coordinator(state, RuntimeRegistry([runtime]))
    coordinator.switch_host(5, "server-a")
    first = await coordinator.create_session(5, workdir="/one", name="one")
    second = await coordinator.create_session(5, workdir="/two", name="two")

    await coordinator.archive_session(5, second.id)

    assert coordinator.selected_session(5) == first


@pytest.mark.asyncio
async def test_user_can_switch_between_host_first_and_global_session_views() -> None:
    state = ProjectState.empty("local")
    state.hosts["server-a"] = Host(id="server-a", name="Build server")
    state.hosts["server-b"] = Host(id="server-b", name="GPU server")
    coordinator = Coordinator(
        state,
        RuntimeRegistry([MemoryRuntime("server-a"), MemoryRuntime("server-b")]),
    )
    coordinator.switch_host(10, "server-a")
    session_a1 = await coordinator.create_session(
        10, workdir="/srv/a", name="backend"
    )
    session_a2 = await coordinator.create_session(
        10, workdir="/srv/a-docs", name="documentation"
    )
    coordinator.switch_host(10, "server-b")
    session_b = await coordinator.create_session(
        10, workdir="/srv/b", name="training"
    )
    session_a1.created_at = 100.0
    session_a2.created_at = 200.0
    session_b.created_at = 300.0

    coordinator.switch_host(10, "server-a")
    coordinator.activate_session(10, session_a2.id)

    host_first = coordinator.session_listing(10)
    global_view = coordinator.set_session_view_mode(
        10, SessionViewMode.ALL_HOSTS
    )

    assert host_first.mode is SessionViewMode.HOST_FIRST
    assert [item.session.id for item in host_first.items] == [
        session_a1.id,
        session_a2.id,
    ]
    assert host_first.items[1].is_selected
    assert {item.qualified_name for item in global_view.items} == {
        "backend · Build server",
        "documentation · Build server",
        "training · GPU server",
    }
    assert [item.session.id for item in global_view.items] == [
        session_a1.id,
        session_a2.id,
        session_b.id,
    ]
    assert global_view.items[1].is_selected

    coordinator.activate_session(10, session_b.id)

    assert coordinator.session_listing(10).selected_host.id == "server-b"
    assert state.preferences.session_view(11) is SessionViewMode.HOST_FIRST
