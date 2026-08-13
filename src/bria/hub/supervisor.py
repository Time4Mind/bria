from __future__ import annotations

import asyncio
import contextlib
import logging
from collections.abc import AsyncIterator, Callable
from typing import Protocol

from ..protocol.envelope import Envelope
from ..protocol.messages import Hello
from ..runtime.remote import RemoteRuntime
from ..transport.base import HubTransport
from .event_consumer import HubEventConsumer
from .service import HubService

logger = logging.getLogger(__name__)


class ConnectedHostChannel(HubTransport, Protocol):
    @property
    def hello(self) -> Hello: ...

    async def wait_closed(self) -> None: ...

    def notifications(self) -> AsyncIterator[Envelope]: ...

    async def acknowledge_event(self, sequence: int) -> None: ...


class HostChannelSource(Protocol):
    async def wait_for_host(
        self, host_id: str, *, timeout: float | None = None
    ) -> ConnectedHostChannel: ...


class HubConnectionSupervisor:
    """Bind authenticated transport channels to hub runtime lifecycles."""

    def __init__(
        self,
        hub: HubService,
        channels: HostChannelSource,
        *,
        runtime_factory: Callable[[HubTransport], RemoteRuntime] = RemoteRuntime,
    ) -> None:
        self.hub = hub
        self.channels = channels
        self.runtime_factory = runtime_factory
        self._tasks: dict[str, asyncio.Task[None]] = {}
        self._closing = False

    def supervise(self, host_id: str) -> None:
        if self._closing:
            raise RuntimeError("connection supervisor is closing")
        task = self._tasks.get(host_id)
        if task is not None and not task.done():
            return
        self._tasks[host_id] = asyncio.create_task(
            self._run_host(host_id), name=f"supervise-host:{host_id}"
        )

    async def close(self) -> None:
        self._closing = True
        tasks = tuple(self._tasks.values())
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        self._tasks.clear()

    async def _run_host(self, host_id: str) -> None:
        while True:
            channel: ConnectedHostChannel | None = None
            try:
                channel = await self.channels.wait_for_host(host_id, timeout=None)
                hello = channel.hello
                runtime = self.runtime_factory(channel)
                async with self.hub.mutation_lock:
                    self.hub.connect_runtime(
                        runtime,
                        name=hello.host_name or host_id,
                        home_dir=hello.home_dir,
                        agent_version=hello.agent_version,
                    )
                    await self.hub.synchronize(host_id)
                await self._consume_events(channel)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("host %s channel failed: %s", host_id, exc)
                if channel is not None:
                    with contextlib.suppress(Exception):
                        await channel.close()
                await asyncio.sleep(0)
            finally:
                async with self.hub.mutation_lock:
                    self.hub.disconnect_runtime(host_id)

    async def _consume_events(self, channel: ConnectedHostChannel) -> None:
        consumer = HubEventConsumer(
            self.hub.state,
            self.hub.store,
            host_id=channel.host_id,
            after_commit=self.hub.events.publish,
        )
        async for event in channel.notifications():
            async with self.hub.mutation_lock:
                acknowledgement = consumer.consume(event)
            await channel.acknowledge_event(acknowledgement.sequence)
