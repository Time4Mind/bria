from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass

from ..protocol.envelope import Envelope
from ..protocol.messages import Event


@dataclass(frozen=True, slots=True)
class AppliedHostEvent:
    envelope: Envelope
    event: Event


class HubEventStream:
    """Best-effort post-commit fan-out; durable ACK never depends on sinks."""

    def __init__(self, *, subscriber_capacity: int = 128) -> None:
        if subscriber_capacity < 1:
            raise ValueError("subscriber_capacity must be positive")
        self.subscriber_capacity = subscriber_capacity
        self._subscribers: set[asyncio.Queue[AppliedHostEvent]] = set()

    def publish(self, applied: AppliedHostEvent) -> None:
        for queue in tuple(self._subscribers):
            if queue.full():
                queue.get_nowait()
            queue.put_nowait(applied)

    @asynccontextmanager
    async def subscribe(self) -> AsyncIterator[AsyncIterator[AppliedHostEvent]]:
        queue: asyncio.Queue[AppliedHostEvent] = asyncio.Queue(
            self.subscriber_capacity
        )
        self._subscribers.add(queue)
        try:
            yield _queue_events(queue)
        finally:
            self._subscribers.discard(queue)


async def _queue_events(
    queue: asyncio.Queue[AppliedHostEvent],
) -> AsyncIterator[AppliedHostEvent]:
    while True:
        yield await queue.get()
