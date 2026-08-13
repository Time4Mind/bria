from __future__ import annotations

import asyncio
from pathlib import Path

from bria.agent.event_spool import EventSpool
from bria.agent.protocol_handler import AgentProtocolHandler
from bria.agent.service import AgentService
from bria.protocol.envelope import Envelope, MessageKind
from bria.protocol.messages import Event, EventName
from bria.runtime.memory import MemoryRuntime
from bria.transport import (
    AgentConnectionRunner,
    StaticTokenValidator,
    WebSocketAgentTransport,
    WebSocketHubServer,
    WebSocketSettings,
)

HOST_ID = "server-a"
TOKEN = "event-test-token"


async def test_pending_events_replay_and_cumulative_ack(tmp_path: Path) -> None:
    spool_path = tmp_path / "events.json"
    spool = EventSpool(spool_path, host_id=HOST_ID)
    spool.append(Event(EventName.HOST_STATUS, data={"status": "offline"}))
    spool.append(Event(EventName.HOST_STATUS, data={"status": "online"}))
    spool.acknowledge(1)
    settings = WebSocketSettings(request_timeout=2, hello_timeout=2)
    server = WebSocketHubServer(
        StaticTokenValidator({HOST_ID: TOKEN}), settings=settings
    )
    await server.start()
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
        settings=settings,
    )
    hello = Envelope(
        kind=MessageKind.HELLO,
        host_id=HOST_ID,
        payload=(await service.hello()).to_payload(),
    )
    runner = AgentConnectionRunner(
        transport,
        AgentProtocolHandler(service),
        hello,
        event_spool=spool,
    )
    agent_task = asyncio.create_task(runner.run())
    try:
        channel = await server.wait_for_host(HOST_ID, timeout=2)
        assert channel.hello.last_acked_sequence == 1
        notifications = channel.notifications()
        replayed = await anext(notifications)
        assert replayed.kind is MessageKind.EVENT
        assert replayed.sequence == 2

        published = await runner.publish_event(
            Event(
                EventName.SESSION_CHANGED,
                session_id="session-1",
                data={"state": "active"},
            )
        )
        live = await anext(notifications)
        assert live.sequence == published.sequence == 3

        await channel.acknowledge_event(3)
        async with asyncio.timeout(2):
            while spool.acked_sequence != 3:
                await asyncio.sleep(0)
        assert spool.pending() == ()
        assert EventSpool(spool_path, host_id=HOST_ID).pending() == ()
        assert not channel.closed
    finally:
        await runner.close()
        await asyncio.gather(agent_task, return_exceptions=True)
        await server.close()
