from __future__ import annotations

from bria.config import Settings
from bria.domain.enums import SessionState
from bria.hub.service import HubService
from bria.runtime.base import CreateSessionRequest
from bria.runtime.memory import MemoryRuntime


async def test_hub_reconciles_agent_snapshot(tmp_path) -> None:
    settings = Settings(tmp_path, "local", "Hub", "", "")
    hub = HubService.load(settings)
    runtime = MemoryRuntime("server-a")
    await runtime.create_session(
        CreateSessionRequest("remote-1", "/srv/app", "Remote app")
    )

    hub.connect_runtime(runtime, name="Server A", home_dir="/home/agent")
    host = await hub.synchronize("server-a")

    assert host.name == "Server A"
    assert host.runtime_version == "memory"
    assert hub.state.sessions["remote-1"].host_id == "server-a"
    assert hub.state.sessions["remote-1"].workdir == "/srv/app"


async def test_hub_marks_missing_live_session_lost(tmp_path) -> None:
    settings = Settings(tmp_path, "local", "Hub", "", "")
    hub = HubService.load(settings)
    runtime = MemoryRuntime("server-a")
    hub.connect_runtime(runtime, name="Server A")
    await runtime.create_session(
        CreateSessionRequest("remote-1", "/srv/app", "Remote app")
    )
    await hub.synchronize("server-a")
    runtime.sessions.clear()

    await hub.synchronize("server-a")

    assert hub.state.sessions["remote-1"].state is SessionState.LOST
    assert hub.state.sessions["remote-1"].window_id == ""


async def test_hub_reconciles_externally_restored_session_timestamp(tmp_path) -> None:
    settings = Settings(tmp_path, "local", "Hub", "", "")
    hub = HubService.load(settings)
    runtime = MemoryRuntime("server-a")
    hub.connect_runtime(runtime, name="Server A")
    await runtime.create_session(
        CreateSessionRequest("remote-1", "/srv/app", "Remote app")
    )
    await hub.synchronize("server-a")
    session = hub.state.sessions["remote-1"]
    session.state = SessionState.LOST
    session.window_id = ""

    await hub.synchronize("server-a")

    assert session.state is SessionState.ACTIVE
    assert session.restored_at > 0
    assert session.live_since_at == session.restored_at
