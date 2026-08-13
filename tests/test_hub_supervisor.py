from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator

from bria.agent.protocol_handler import AgentProtocolHandler
from bria.agent.service import AgentService
from bria.config import Settings
from bria.domain.enums import HostStatus
from bria.hub.service import HubService
from bria.hub.supervisor import HubConnectionSupervisor
from bria.protocol.envelope import Envelope
from bria.protocol.messages import Hello
from bria.protocol.version import PROTOCOL_VERSION
from bria.runtime.base import CreateSessionRequest
from bria.runtime.memory import MemoryRuntime


class LoopbackChannel:
    def __init__(self, runtime: MemoryRuntime) -> None:
        self._runtime = runtime
        self._handler = AgentProtocolHandler(
            AgentService(
                runtime,
                host_name="Build server",
                home_dir="/home/agent",
                agent_version="test-agent",
            )
        )
        self._closed = asyncio.Event()
        self._notifications: asyncio.Queue[Envelope | None] = asyncio.Queue()
        self._hello = Hello(
            agent_version="test-agent",
            protocol_version=PROTOCOL_VERSION,
            host_name="Build server",
            home_dir="/home/agent",
        )

    @property
    def host_id(self) -> str:
        return self._runtime.host_id

    @property
    def hello(self) -> Hello:
        return self._hello

    async def request(self, message: Envelope) -> Envelope:
        return await self._handler.handle(message)

    async def wait_closed(self) -> None:
        await self._closed.wait()

    async def notifications(self) -> AsyncIterator[Envelope]:
        while True:
            message = await self._notifications.get()
            if message is None:
                return
            yield message

    async def acknowledge_event(self, sequence: int) -> None:
        assert sequence > 0

    async def close(self) -> None:
        self._closed.set()
        await self._notifications.put(None)


class ChannelQueue:
    def __init__(self) -> None:
        self.queue: asyncio.Queue[LoopbackChannel] = asyncio.Queue()

    async def wait_for_host(
        self, host_id: str, *, timeout: float | None = None
    ) -> LoopbackChannel:
        del timeout
        channel = await self.queue.get()
        assert channel.host_id == host_id
        return channel


async def _wait_until(predicate, attempts: int = 100) -> None:
    for _ in range(attempts):
        if predicate():
            return
        await asyncio.sleep(0.001)
    raise AssertionError("condition wasn't reached")


async def test_supervisor_connects_synchronizes_and_disconnects_host(tmp_path) -> None:
    hub = HubService.load(Settings(tmp_path, "local", "Hub", "", ""))
    runtime = MemoryRuntime("server-a")
    await runtime.create_session(
        CreateSessionRequest("remote-1", "/srv/app", "Remote app")
    )
    channel = LoopbackChannel(runtime)
    channels = ChannelQueue()
    supervisor = HubConnectionSupervisor(hub, channels)
    supervisor.supervise("server-a")

    await channels.queue.put(channel)
    await _wait_until(lambda: "remote-1" in hub.state.sessions)

    assert hub.state.hosts["server-a"].status is HostStatus.ONLINE
    assert hub.state.hosts["server-a"].home_dir == "/home/agent"
    assert hub.state.sessions["remote-1"].host_id == "server-a"

    await channel.close()
    await _wait_until(
        lambda: hub.state.hosts["server-a"].status is HostStatus.OFFLINE
    )
    await supervisor.close()
