from __future__ import annotations

import asyncio
import contextlib
import logging
import signal
from pathlib import Path

from .. import __version__
from ..config import Settings
from ..protocol.envelope import Envelope, MessageKind
from ..runtime.local_runtime import LocalRuntime
from ..runtime.provider_binding import TranscriptPathPolicy
from ..runtime.transcript_source import JsonlTranscriptSource
from ..transport.agent import AgentConnectionRunner, WebSocketAgentTransport
from ..transport.tls import client_ssl_context
from .event_spool import EventSpool
from .local_event_monitor import LocalEventMonitor
from .local_monitor_state import LocalMonitorStateStore
from .protocol_handler import AgentProtocolHandler
from .service import AgentService

logger = logging.getLogger(__name__)


async def run_agent(settings: Settings) -> int:
    if not settings.hub_url:
        raise ValueError("BRIA_HUB_URL is required")
    if not settings.agent_token:
        raise ValueError("BRIA_AGENT_TOKEN is required")
    runtime = LocalRuntime(
        settings.host_id,
        registry_path=settings.runtime_registry_file(settings.host_id),
        tmux_session_name=settings.tmux_session_name,
        backend_commands={
            "claude": settings.claude_command,
            "codex": settings.codex_command,
        },
        hook_environment={"BRIA_DATA_DIR": str(settings.data_dir)},
    )
    service = AgentService(
        runtime,
        host_name=settings.host_name,
        home_dir=str(Path.home()),
        agent_version=__version__,
    )
    shutdown = _shutdown_event()
    event_spool = EventSpool(
        settings.event_spool_file(settings.host_id), host_id=settings.host_id
    )
    backoff = 1.0
    while not shutdown.is_set():
        try:
            await _run_connection(
                settings,
                service,
                runtime,
                event_spool,
                shutdown,
            )
            backoff = 1.0
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            logger.warning("hub connection failed: %s", exc)
        if not shutdown.is_set():
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(shutdown.wait(), timeout=backoff)
            backoff = min(backoff * 2, 30.0)
    return 0


async def _run_connection(
    settings: Settings,
    service: AgentService,
    runtime: LocalRuntime,
    event_spool: EventSpool,
    shutdown: asyncio.Event,
) -> None:
    transport = WebSocketAgentTransport(
        settings.hub_url,
        host_id=settings.host_id,
        token=settings.agent_token,
        ssl_context=client_ssl_context(settings.tls_ca_file),
    )
    hello = Envelope(
        kind=MessageKind.HELLO,
        host_id=settings.host_id,
        payload=(await service.hello()).to_payload(),
    )
    runner = AgentConnectionRunner(
        transport,
        AgentProtocolHandler(service),
        hello,
        event_spool=event_spool,
    )
    monitor = LocalEventMonitor(
        runtime,
        JsonlTranscriptSource(TranscriptPathPolicy.defaults()),
        LocalMonitorStateStore(settings.event_monitor_file(settings.host_id)),
        runner.publish_event,
        poll_interval=settings.event_poll_interval,
    )
    connection = asyncio.create_task(runner.run(), name="agent-connection")
    monitoring = asyncio.create_task(monitor.run(), name="agent-local-events")
    stopping = asyncio.create_task(shutdown.wait(), name="agent-shutdown")
    done, _ = await asyncio.wait(
        {connection, monitoring, stopping}, return_when=asyncio.FIRST_COMPLETED
    )
    if stopping in done:
        await runner.close()
        connection.cancel()
        monitoring.cancel()
        await asyncio.gather(
            connection, monitoring, stopping, return_exceptions=True
        )
        return
    stopping.cancel()
    monitoring.cancel()
    await asyncio.gather(stopping, monitoring, return_exceptions=True)
    if connection not in done:
        await runner.close()
        connection.cancel()
        await asyncio.gather(connection, return_exceptions=True)
        raise RuntimeError("local event monitor stopped unexpectedly")
    else:
        await connection


def _shutdown_event() -> asyncio.Event:
    event = asyncio.Event()
    loop = asyncio.get_running_loop()
    for signum in (signal.SIGINT, signal.SIGTERM):
        with contextlib.suppress(NotImplementedError):
            loop.add_signal_handler(signum, event.set)
    return event
