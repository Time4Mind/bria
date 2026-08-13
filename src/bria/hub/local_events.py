from __future__ import annotations

from ..agent.event_spool import EventSpool
from ..agent.local_event_monitor import LocalEventMonitor
from ..agent.local_monitor_state import LocalMonitorStateStore
from ..config import Settings
from ..protocol.messages import Event
from ..runtime.local_runtime import LocalRuntime
from ..runtime.provider_binding import TranscriptPathPolicy
from ..runtime.transcript_source import JsonlTranscriptSource
from .event_consumer import HubEventConsumer
from .service import HubService


class HubLocalEventBridge:
    """Give the embedded local runtime the same durable event semantics."""

    def __init__(
        self, hub: HubService, runtime: LocalRuntime, settings: Settings
    ) -> None:
        host_id = runtime.host_id
        self.hub = hub
        self.spool = EventSpool(
            settings.event_spool_file(host_id), host_id=host_id
        )
        self.consumer = HubEventConsumer(
            hub.state,
            hub.store,
            host_id=host_id,
            after_commit=hub.events.publish,
        )
        self.monitor = LocalEventMonitor(
            runtime,
            JsonlTranscriptSource(TranscriptPathPolicy.defaults()),
            LocalMonitorStateStore(settings.event_monitor_file(host_id)),
            self.publish,
            poll_interval=settings.event_poll_interval,
        )

    async def run(self) -> None:
        await self._drain()
        await self.monitor.run()

    async def publish(self, event: Event) -> object:
        envelope = self.spool.append(event)
        await self._drain()
        return envelope

    async def _drain(self) -> None:
        for envelope in self.spool.pending():
            async with self.hub.mutation_lock:
                acknowledgement = self.consumer.consume(envelope)
            self.spool.acknowledge(acknowledgement.sequence)
