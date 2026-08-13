from __future__ import annotations

import pytest

from bria.domain.host import Host
from bria.domain.project_state import ProjectState
from bria.hub.event_consumer import HubEventConsumer
from bria.hub.event_stream import HubEventStream
from bria.protocol.envelope import Envelope, MessageKind
from bria.protocol.messages import Event, EventName


class Store:
    def load(self) -> ProjectState:
        raise NotImplementedError

    def save(self, state: ProjectState) -> None:
        del state


@pytest.mark.asyncio
async def test_event_stream_fans_out_only_after_durable_consume() -> None:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host("server-a", "Server A")
    stream = HubEventStream()
    consumer = HubEventConsumer(
        state,
        Store(),
        host_id="server-a",
        after_commit=stream.publish,
    )
    envelope = Envelope(
        kind=MessageKind.EVENT,
        host_id="server-a",
        sequence=1,
        payload=Event(
            EventName.SESSION_ANNOUNCED,
            session_id="session-a",
            data={"name": "Build", "workdir": "/srv/build"},
        ).to_payload(),
    )

    async with stream.subscribe() as events:
        consumer.consume(envelope)
        applied = await anext(events)

    assert applied.envelope.sequence == 1
    assert applied.event.session_id == "session-a"
