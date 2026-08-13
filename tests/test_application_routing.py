from __future__ import annotations

import pytest

from bria.control.coordinator import Coordinator, NoActiveSessionError
from bria.domain.host import Host
from bria.domain.project_state import ProjectState
from bria.runtime.base import DirectoryEntry
from bria.runtime.memory import MemoryRuntime
from bria.runtime.registry import RuntimeRegistry


class DirectoryRuntime(MemoryRuntime):
    def __init__(self, host_id: str) -> None:
        super().__init__(host_id)
        self.directory_requests: list[str] = []

    async def list_directories(self, path: str) -> list[DirectoryEntry]:
        self.directory_requests.append(path)
        return [DirectoryEntry(name=self.host_id, path=f"{path}/{self.host_id}")]


def make_coordinator() -> tuple[Coordinator, DirectoryRuntime, DirectoryRuntime]:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    state.hosts["server-b"] = Host(id="server-b", name="Server B")
    runtime_a = DirectoryRuntime("server-a")
    runtime_b = DirectoryRuntime("server-b")
    coordinator = Coordinator(state, RuntimeRegistry([runtime_a, runtime_b]))
    return coordinator, runtime_a, runtime_b


@pytest.mark.asyncio
async def test_selected_session_operations_route_to_its_host() -> None:
    coordinator, runtime_a, runtime_b = make_coordinator()
    coordinator.switch_host(1, "server-a")
    session_a = await coordinator.create_session(1, workdir="/srv/a", name="alpha")
    coordinator.switch_host(1, "server-b")
    session_b = await coordinator.create_session(1, workdir="/srv/b", name="beta")

    sent_to = await coordinator.send_key(1, "Enter")
    capture = await coordinator.capture_selected(1)
    uploaded_path = await coordinator.upload_file(1, "brief.txt", b"content")

    assert sent_to is session_b
    assert runtime_b.sent_keys == [(session_b.id, "Enter")]
    assert runtime_a.sent_keys == []
    assert capture.text == "server-b:beta"
    assert uploaded_path == ".ccbot-inbox/brief.txt"
    assert runtime_b.files[(session_b.id, "brief.txt")] == b"content"
    assert (session_a.id, "brief.txt") not in runtime_a.files


@pytest.mark.asyncio
@pytest.mark.parametrize("operation", ["text", "key", "capture", "upload"])
async def test_selected_session_operations_require_live_session(
    operation: str,
) -> None:
    coordinator, _, _ = make_coordinator()
    coordinator.switch_host(2, "server-a")

    with pytest.raises(NoActiveSessionError, match="no active session"):
        if operation == "text":
            await coordinator.send_text(2, "hello")
        elif operation == "key":
            await coordinator.send_key(2, "Escape")
        elif operation == "capture":
            await coordinator.capture_selected(2)
        else:
            await coordinator.upload_file(2, "file.txt", b"data")


@pytest.mark.asyncio
async def test_directory_listing_routes_to_host_without_active_session() -> None:
    coordinator, runtime_a, runtime_b = make_coordinator()
    coordinator.switch_host(3, "server-a")

    entries = await coordinator.list_directories(3, "/srv")

    assert entries == [DirectoryEntry(name="server-a", path="/srv/server-a")]
    assert runtime_a.directory_requests == ["/srv"]
    assert runtime_b.directory_requests == []
