from __future__ import annotations

import asyncio
import contextlib
import ssl
from collections.abc import AsyncIterator
from dataclasses import replace

from websockets.asyncio.client import ClientConnection, connect
from websockets.exceptions import ConnectionClosed

from ..protocol.envelope import Envelope, MessageKind, ProtocolError
from ..protocol.messages import Event
from .base import EnvelopeHandler, OutboundEventSpool, TransportClosed
from .codec import decode_envelope
from .settings import HOST_ID_HEADER, SUBPROTOCOL, WebSocketSettings


class WebSocketAgentTransport:
    """One authenticated outbound agent-to-hub WebSocket connection."""

    def __init__(
        self,
        uri: str,
        *,
        host_id: str,
        token: str,
        settings: WebSocketSettings | None = None,
        ssl_context: ssl.SSLContext | None = None,
    ) -> None:
        if not host_id.strip():
            raise ValueError("host_id is required")
        if not token:
            raise ValueError("agent token is required")
        self.uri = uri
        self.host_id = host_id
        self._token = token
        self.settings = settings or WebSocketSettings()
        self.ssl_context = ssl_context
        self._connection: ClientConnection | None = None
        self._send_lock = asyncio.Lock()

    @property
    def connected(self) -> bool:
        return self._connection is not None

    async def connect(self) -> None:
        if self._connection is not None:
            return
        self._connection = await connect(
            self.uri,
            additional_headers={
                "Authorization": f"Bearer {self._token}",
                HOST_ID_HEADER: self.host_id,
            },
            subprotocols=[SUBPROTOCOL],
            open_timeout=self.settings.open_timeout,
            close_timeout=self.settings.close_timeout,
            ping_interval=self.settings.ping_interval,
            ping_timeout=self.settings.ping_timeout,
            max_size=self.settings.max_message_size,
            ssl=self.ssl_context,
        )
        if self._connection.subprotocol != SUBPROTOCOL:
            await self.close()
            raise TransportClosed("hub didn't negotiate the Bria protocol")

    async def send(self, message: Envelope) -> None:
        connection = self._require_connection()
        if message.host_id != self.host_id:
            raise ValueError("message targets a different host")
        try:
            async with self._send_lock:
                await connection.send(message.to_json())
        except ConnectionClosed as exc:
            raise TransportClosed("agent connection is closed") from exc

    async def messages(self) -> AsyncIterator[Envelope]:
        connection = self._require_connection()
        try:
            async for frame in connection:
                yield decode_envelope(frame)
        except ConnectionClosed as exc:
            raise TransportClosed("agent connection is closed") from exc

    async def close(self) -> None:
        connection, self._connection = self._connection, None
        if connection is None:
            return
        await connection.close()
        with contextlib.suppress(TimeoutError):
            await asyncio.wait_for(
                connection.wait_closed(), self.settings.close_timeout
            )

    def _require_connection(self) -> ClientConnection:
        if self._connection is None:
            raise TransportClosed("agent isn't connected")
        return self._connection


class AgentConnectionRunner:
    """Dispatch hub commands concurrently while keeping transport generic."""

    def __init__(
        self,
        transport: WebSocketAgentTransport,
        handler: EnvelopeHandler,
        hello: Envelope,
        *,
        event_spool: OutboundEventSpool | None = None,
    ) -> None:
        if hello.kind is not MessageKind.HELLO:
            raise ValueError("initial envelope must be hello")
        if hello.host_id != transport.host_id:
            raise ValueError("hello targets a different host")
        if event_spool is not None and event_spool.host_id != transport.host_id:
            raise ValueError("event spool belongs to a different host")
        self.transport = transport
        self.handler = handler
        self.event_spool = event_spool
        hello_payload = dict(hello.payload)
        if event_spool is not None:
            hello_payload["last_acked_sequence"] = event_spool.acked_sequence
        self.hello = replace(hello, payload=hello_payload)
        self._tasks: set[asyncio.Task[None]] = set()
        self._semaphore = asyncio.Semaphore(
            transport.settings.max_inflight_commands
        )
        self._event_lock = asyncio.Lock()
        self._events_live = False

    async def run(self) -> None:
        await self.transport.connect()
        await self.transport.send(self.hello)
        await self._start_event_delivery()
        try:
            async for message in self.transport.messages():
                if message.kind is MessageKind.ACK:
                    await self._acknowledge_event(message)
                    continue
                await self._semaphore.acquire()
                task = asyncio.create_task(self._handle(message))
                self._tasks.add(task)
                task.add_done_callback(self._task_done)
        finally:
            async with self._event_lock:
                self._events_live = False
            await self._finish_tasks()
            await self.transport.close()

    async def close(self) -> None:
        async with self._event_lock:
            self._events_live = False
        await self.transport.close()
        await self._finish_tasks()

    async def publish_event(self, event: Event) -> Envelope:
        if self.event_spool is None:
            raise RuntimeError("event spool isn't configured")
        async with self._event_lock:
            envelope = self.event_spool.append(event)
            if self._events_live:
                await self.transport.send(envelope)
            return envelope

    async def _start_event_delivery(self) -> None:
        async with self._event_lock:
            if self.event_spool is not None:
                for envelope in self.event_spool.pending():
                    await self.transport.send(envelope)
            self._events_live = True

    async def _acknowledge_event(self, message: Envelope) -> None:
        if message.host_id != self.transport.host_id:
            raise ProtocolError("ACK came from a different host channel")
        if message.request_id:
            raise ProtocolError("event ACK must not contain request_id")
        payload_sequence = int(
            message.payload.get("acked_sequence", message.sequence)
        )
        if message.sequence <= 0 or payload_sequence != message.sequence:
            raise ProtocolError("event ACK has an invalid sequence")
        async with self._event_lock:
            if self.event_spool is not None:
                try:
                    self.event_spool.acknowledge(message.sequence)
                except ValueError as exc:
                    raise ProtocolError("event ACK is outside the spool") from exc

    async def _handle(self, message: Envelope) -> None:
        try:
            try:
                response = await self.handler.handle(message)
            except Exception:
                response = Envelope(
                    kind=MessageKind.ERROR,
                    host_id=self.transport.host_id,
                    request_id=message.request_id,
                    payload={
                        "code": "internal_error",
                        "message": "agent handler failed",
                        "retryable": True,
                    },
                )
            await self.transport.send(response)
        finally:
            self._semaphore.release()

    def _task_done(self, task: asyncio.Task[None]) -> None:
        self._tasks.discard(task)
        if not task.cancelled():
            task.exception()

    async def _finish_tasks(self) -> None:
        if not self._tasks:
            return
        tasks = tuple(self._tasks)
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
