from __future__ import annotations

import asyncio
import contextlib
import re
import ssl
from collections.abc import AsyncIterator
from http import HTTPStatus

from websockets.asyncio.server import Server, ServerConnection, serve
from websockets.exceptions import ConnectionClosed
from websockets.http11 import Request, Response

from ..protocol.envelope import Envelope, MessageKind, ProtocolError
from ..protocol.messages import Hello
from ..protocol.version import PROTOCOL_VERSION
from .authentication import TokenValidator, bearer_token
from .base import TransportClosed
from .codec import decode_envelope
from .settings import HOST_ID_HEADER, SUBPROTOCOL, WebSocketSettings

_VALID_HOST_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")


class WebSocketHubChannel:
    """Correlated RPC channel bound to one authenticated host."""

    def __init__(
        self,
        connection: ServerConnection,
        *,
        host_id: str,
        hello: Hello,
        settings: WebSocketSettings,
    ) -> None:
        self._connection = connection
        self._host_id = host_id
        self.hello = hello
        self.settings = settings
        self._pending: dict[str, asyncio.Future[Envelope]] = {}
        self._notifications: asyncio.Queue[Envelope | None] = asyncio.Queue()
        self._send_lock = asyncio.Lock()
        self._closed = asyncio.Event()
        self._reader: asyncio.Task[None] | None = None

    @property
    def host_id(self) -> str:
        return self._host_id

    @property
    def closed(self) -> bool:
        return self._closed.is_set()

    def start(self) -> None:
        if self._reader is None:
            self._reader = asyncio.create_task(
                self._read_messages(), name=f"hub-reader:{self.host_id}"
            )

    async def request(self, message: Envelope) -> Envelope:
        if message.host_id != self.host_id:
            raise ValueError("message targets a different host")
        if message.kind not in {MessageKind.COMMAND, MessageKind.HEARTBEAT}:
            raise ValueError("hub requests must be command or heartbeat envelopes")
        if not message.request_id:
            raise ValueError("request_id is required")
        if self.closed:
            raise TransportClosed("host channel is closed")
        loop = asyncio.get_running_loop()
        future: asyncio.Future[Envelope] = loop.create_future()
        if message.request_id in self._pending:
            raise ValueError("request_id is already in flight")
        self._pending[message.request_id] = future
        try:
            async with self._send_lock:
                await self._connection.send(message.to_json())
            async with asyncio.timeout(self.settings.request_timeout):
                return await future
        except TimeoutError as exc:
            raise TimeoutError(
                f"request to host {self.host_id} timed out"
            ) from exc
        except ConnectionClosed as exc:
            raise TransportClosed("host channel is closed") from exc
        finally:
            self._pending.pop(message.request_id, None)
            if not future.done():
                future.cancel()

    async def notifications(self) -> AsyncIterator[Envelope]:
        while True:
            message = await self._notifications.get()
            if message is None:
                return
            yield message

    async def acknowledge_event(self, sequence: int) -> None:
        """Send a cumulative ACK after the consumer durably handles an event."""
        if sequence <= 0:
            raise ValueError("event ACK sequence must be positive")
        if self.closed:
            raise TransportClosed("host channel is closed")
        message = Envelope(
            kind=MessageKind.ACK,
            host_id=self.host_id,
            sequence=sequence,
            payload={"acked_sequence": sequence},
        )
        try:
            async with self._send_lock:
                await self._connection.send(message.to_json())
        except ConnectionClosed as exc:
            raise TransportClosed("host channel is closed") from exc

    async def wait_closed(self) -> None:
        await self._closed.wait()

    async def close(self) -> None:
        if not self._closed.is_set():
            await self._connection.close(code=1000, reason="channel closed")
        reader = self._reader
        if reader is not None and reader is not asyncio.current_task():
            with contextlib.suppress(asyncio.CancelledError, TimeoutError):
                await asyncio.wait_for(reader, self.settings.close_timeout)
        self._finish(TransportClosed("host channel is closed"))

    async def _read_messages(self) -> None:
        failure: Exception = TransportClosed("host channel is closed")
        try:
            async for frame in self._connection:
                message = decode_envelope(frame)
                if message.host_id != self.host_id:
                    raise ProtocolError("message came from a different host")
                if (
                    message.kind is MessageKind.EVENT
                    and message.sequence <= 0
                ):
                    raise ProtocolError("event sequence must be positive")
                if message.kind in {
                    MessageKind.RESULT,
                    MessageKind.ERROR,
                    MessageKind.ACK,
                }:
                    future = self._pending.get(message.request_id)
                    if future is None:
                        raise ProtocolError("response has no matching request")
                    if not future.done():
                        future.set_result(message)
                else:
                    await self._notifications.put(message)
        except (ConnectionClosed, ProtocolError) as exc:
            failure = TransportClosed("host channel failed")
            failure.__cause__ = exc
            if isinstance(exc, ProtocolError):
                await self._connection.close(code=1002, reason="protocol error")
        finally:
            self._finish(failure)

    def _finish(self, failure: Exception) -> None:
        if self._closed.is_set():
            return
        self._closed.set()
        for future in self._pending.values():
            if not future.done():
                future.set_exception(failure)
        self._notifications.put_nowait(None)


class WebSocketHubServer:
    """Authenticated WebSocket listener and registry of connected hosts."""

    def __init__(
        self,
        authenticate: TokenValidator,
        *,
        bind_host: str = "127.0.0.1",
        port: int = 0,
        settings: WebSocketSettings | None = None,
        ssl_context: ssl.SSLContext | None = None,
    ) -> None:
        self.authenticate = authenticate
        self.bind_host = bind_host
        self.requested_port = port
        self.settings = settings or WebSocketSettings()
        self.ssl_context = ssl_context
        self._server: Server | None = None
        self._channels: dict[str, WebSocketHubChannel] = {}
        self._changed = asyncio.Condition()

    @property
    def port(self) -> int:
        server = self._server
        if server is None or not server.sockets:
            raise RuntimeError("hub WebSocket server isn't running")
        return int(server.sockets[0].getsockname()[1])

    async def start(self) -> None:
        if self._server is not None:
            return
        self._server = await serve(
            self._handle_connection,
            self.bind_host,
            self.requested_port,
            process_request=self._authenticate_request,
            subprotocols=[SUBPROTOCOL],
            open_timeout=self.settings.open_timeout,
            close_timeout=self.settings.close_timeout,
            ping_interval=self.settings.ping_interval,
            ping_timeout=self.settings.ping_timeout,
            max_size=self.settings.max_message_size,
            ssl=self.ssl_context,
        )

    def channel(self, host_id: str) -> WebSocketHubChannel | None:
        channel = self._channels.get(host_id)
        return channel if channel is not None and not channel.closed else None

    async def wait_for_host(
        self, host_id: str, *, timeout: float | None = None
    ) -> WebSocketHubChannel:
        async with asyncio.timeout(timeout):
            async with self._changed:
                while (channel := self.channel(host_id)) is None:
                    await self._changed.wait()
                return channel

    async def close(self) -> None:
        server, self._server = self._server, None
        if server is None:
            return
        server.close()
        with contextlib.suppress(TimeoutError):
            await asyncio.wait_for(
                server.wait_closed(), self.settings.close_timeout
            )

    def _authenticate_request(
        self, connection: ServerConnection, request: Request
    ) -> Response | None:
        try:
            host_id = request.headers[HOST_ID_HEADER].strip()
            token = bearer_token(request.headers.get("Authorization"))
            accepted = (
                _VALID_HOST_ID.fullmatch(host_id) is not None
                and token is not None
                and self.authenticate(host_id, token)
            )
        except Exception:
            accepted = False
        if accepted:
            return None
        response = connection.respond(HTTPStatus.UNAUTHORIZED, "Unauthorized\n")
        response.headers["WWW-Authenticate"] = "Bearer"
        return response

    async def _handle_connection(self, connection: ServerConnection) -> None:
        request = connection.request
        if request is None:
            await connection.close(code=1002, reason="missing handshake")
            return
        host_id = request.headers[HOST_ID_HEADER].strip()
        if connection.subprotocol != SUBPROTOCOL:
            await connection.close(code=1002, reason="subprotocol required")
            return
        try:
            async with asyncio.timeout(self.settings.hello_timeout):
                envelope = decode_envelope(await connection.recv())
            hello = Hello.from_payload(envelope.payload)
            if (
                envelope.kind is not MessageKind.HELLO
                or envelope.host_id != host_id
                or hello.protocol_version != PROTOCOL_VERSION
            ):
                raise ProtocolError("invalid agent hello")
        except (ConnectionClosed, ProtocolError, TimeoutError, ValueError):
            await connection.close(code=1002, reason="invalid hello")
            return
        channel = WebSocketHubChannel(
            connection,
            host_id=host_id,
            hello=hello,
            settings=self.settings,
        )
        async with self._changed:
            if self.channel(host_id) is not None:
                await connection.close(code=4009, reason="host already connected")
                return
            self._channels[host_id] = channel
            channel.start()
            self._changed.notify_all()
        try:
            await channel.wait_closed()
        finally:
            async with self._changed:
                if self._channels.get(host_id) is channel:
                    self._channels.pop(host_id, None)
                    self._changed.notify_all()
