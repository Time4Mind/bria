from __future__ import annotations

import asyncio
import contextlib
import logging
import signal

from ..config import Settings
from ..domain.enums import HostKind, HostStatus
from ..domain.host import LOCAL_HOST_ID
from ..runtime.local_runtime import LocalRuntime
from ..security.token_registry import TokenRegistry
from ..transport.hub import WebSocketHubServer
from ..transport.tls import server_ssl_context
from .local_events import HubLocalEventBridge
from .service import HubService
from .supervisor import HubConnectionSupervisor

logger = logging.getLogger(__name__)


async def run_hub(settings: Settings) -> int:
    hub = HubService.load(settings)
    tokens = TokenRegistry(settings.agent_tokens_file)
    server = WebSocketHubServer(
        tokens.verify,
        bind_host=settings.hub_bind_host,
        port=settings.hub_port,
        ssl_context=server_ssl_context(
            settings.tls_cert_file, settings.tls_key_file
        ),
    )
    supervisor = HubConnectionSupervisor(hub, server)
    shutdown = _shutdown_event()
    local_events: asyncio.Task[None] | None = None
    telegram = None
    try:
        await server.start()
        if settings.local_runtime_enabled:
            local_runtime = await _connect_local_runtime(hub, settings)
            local_events = asyncio.create_task(
                HubLocalEventBridge(hub, local_runtime, settings).run(),
                name="hub-local-events",
            )
        else:
            local = hub.state.ensure_local_host(settings.host_name)
            local.enabled = False
            local.status = HostStatus.DISABLED
            hub.store.save(hub.state)
        for host_id in tokens.known_host_ids():
            supervisor.supervise(host_id)
        if settings.telegram_token:
            from ..telegram.app import TelegramAdapter

            telegram = TelegramAdapter(
                hub,
                token=settings.telegram_token,
                allowed_users=settings.allowed_users,
            )
            await telegram.start()
        scheme = "wss" if settings.tls_cert_file is not None else "ws"
        print(
            f"Bria hub listening on "
            f"{scheme}://{settings.hub_bind_host}:{server.port}"
        )
        print(f"supervised remote hosts: {len(tokens.known_host_ids())}")
        print(f"Telegram adapter: {'enabled' if telegram else 'disabled'}")
        await shutdown.wait()
        return 0
    finally:
        if telegram is not None:
            await telegram.close()
        if local_events is not None:
            local_events.cancel()
            await asyncio.gather(local_events, return_exceptions=True)
        await supervisor.close()
        await server.close()


async def _connect_local_runtime(
    hub: HubService, settings: Settings
) -> LocalRuntime:
    runtime = LocalRuntime(
        LOCAL_HOST_ID,
        registry_path=settings.runtime_registry_file(LOCAL_HOST_ID),
        tmux_session_name=settings.tmux_session_name,
        backend_commands={
            "claude": settings.claude_command,
            "codex": settings.codex_command,
        },
        hook_environment={"BRIA_DATA_DIR": str(settings.data_dir)},
    )
    hub.connect_runtime(
        runtime,
        name=settings.host_name,
        kind=HostKind.LOCAL,
    )
    host = await hub.synchronize(LOCAL_HOST_ID)
    if not host.reachable:
        logger.warning("local runtime is unavailable: %s", host.status.value)
    return runtime


def _shutdown_event() -> asyncio.Event:
    event = asyncio.Event()
    loop = asyncio.get_running_loop()
    for signum in (signal.SIGINT, signal.SIGTERM):
        with contextlib.suppress(NotImplementedError):
            loop.add_signal_handler(signum, event.set)
    return event
