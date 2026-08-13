from __future__ import annotations

import asyncio

import pytest
from websockets.exceptions import InvalidStatus

from bria.agent.protocol_handler import AgentProtocolHandler
from bria.agent.service import AgentService
from bria.protocol.envelope import Envelope, MessageKind, ProtocolError
from bria.runtime.base import CreateSessionRequest
from bria.runtime.memory import MemoryRuntime
from bria.runtime.remote import RemoteRuntime
from bria.transport import (
    AgentConnectionRunner,
    StaticTokenValidator,
    WebSocketAgentTransport,
    WebSocketHubServer,
    WebSocketSettings,
)

HOST_ID = "server-a"
TOKEN = "test-token-that-is-not-logged"


async def _agent(
    server: WebSocketHubServer,
) -> tuple[MemoryRuntime, AgentConnectionRunner, asyncio.Task[None]]:
    runtime = MemoryRuntime(HOST_ID)
    service = AgentService(
        runtime,
        host_name="Server A",
        home_dir="/home/bot",
        agent_version="test",
    )
    transport = WebSocketAgentTransport(
        f"ws://127.0.0.1:{server.port}",
        host_id=HOST_ID,
        token=TOKEN,
        settings=server.settings,
    )
    hello = Envelope(
        kind=MessageKind.HELLO,
        host_id=HOST_ID,
        payload=(await service.hello()).to_payload(),
    )
    runner = AgentConnectionRunner(
        transport, AgentProtocolHandler(service), hello
    )
    task = asyncio.create_task(runner.run())
    return runtime, runner, task


async def test_remote_runtime_crosses_authenticated_websocket() -> None:
    settings = WebSocketSettings(request_timeout=2, hello_timeout=2)
    server = WebSocketHubServer(
        StaticTokenValidator({HOST_ID: TOKEN}), settings=settings
    )
    await server.start()
    runtime, runner, agent_task = await _agent(server)
    try:
        channel = await server.wait_for_host(HOST_ID)
        remote = RemoteRuntime(channel)
        first, second = await asyncio.gather(
            remote.create_session(
                CreateSessionRequest("one", "/srv/one", "first")
            ),
            remote.create_session(
                CreateSessionRequest("two", "/srv/two", "second")
            ),
        )
        await remote.send_text(first.session_id, "run tests")
        capture, health = await asyncio.gather(
            remote.capture_pane(second.session_id), remote.health()
        )

        assert {first.session_id, second.session_id} == {"one", "two"}
        assert runtime.sent_text == [("one", "run tests")]
        assert capture.text == "server-a:second"
        assert health.version == "memory"
        assert channel.hello.host_name == "Server A"
    finally:
        await runner.close()
        await asyncio.gather(agent_task, return_exceptions=True)
        await server.close()


async def test_remote_error_envelope_becomes_protocol_error() -> None:
    server = WebSocketHubServer(
        StaticTokenValidator({HOST_ID: TOKEN}),
        settings=WebSocketSettings(request_timeout=2, hello_timeout=2),
    )
    await server.start()
    _, runner, agent_task = await _agent(server)
    try:
        remote = RemoteRuntime(await server.wait_for_host(HOST_ID))
        with pytest.raises(ProtocolError, match="unknown runtime session"):
            await remote.capture_pane("missing")
    finally:
        await runner.close()
        await asyncio.gather(agent_task, return_exceptions=True)
        await server.close()


async def test_invalid_bearer_token_is_rejected_during_handshake() -> None:
    server = WebSocketHubServer(StaticTokenValidator({HOST_ID: TOKEN}))
    await server.start()
    transport = WebSocketAgentTransport(
        f"ws://127.0.0.1:{server.port}",
        host_id=HOST_ID,
        token="wrong-token",
    )
    try:
        with pytest.raises(InvalidStatus) as error:
            await transport.connect()
        assert error.value.response.status_code == 401
        assert TOKEN not in str(error.value)
    finally:
        await transport.close()
        await server.close()
