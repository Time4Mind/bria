from __future__ import annotations

from collections.abc import AsyncIterator
from typing import Protocol

from ..protocol.envelope import Envelope
from ..protocol.messages import Event


class TransportClosed(ConnectionError):
    pass


class EnvelopeHandler(Protocol):
    async def handle(self, envelope: Envelope) -> Envelope: ...


class OutboundEventSpool(Protocol):
    @property
    def host_id(self) -> str: ...

    @property
    def acked_sequence(self) -> int: ...

    def append(self, event: Event) -> Envelope: ...

    def pending(self) -> tuple[Envelope, ...]: ...

    def acknowledge(self, sequence: int) -> bool: ...


class AgentTransport(Protocol):
    """Outbound agent connection to a hub."""

    async def connect(self) -> None: ...

    async def send(self, message: Envelope) -> None: ...

    def messages(self) -> AsyncIterator[Envelope]: ...

    async def close(self) -> None: ...


class HubTransport(Protocol):
    """Hub-side command channel for one authenticated host."""

    @property
    def host_id(self) -> str: ...

    async def request(self, message: Envelope) -> Envelope: ...

    async def close(self) -> None: ...
