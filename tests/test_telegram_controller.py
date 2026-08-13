from __future__ import annotations

from pathlib import Path

import pytest

from bria.config import Settings
from bria.domain.enums import HostKind, SessionViewMode
from bria.hub.service import HubService
from bria.runtime.memory import MemoryRuntime
from bria.telegram.callback_data import callback, entity_token, resolve_token
from bria.telegram.controller import TelegramController


async def configured_controller(
    tmp_path: Path,
) -> tuple[TelegramController, HubService, MemoryRuntime, MemoryRuntime]:
    hub = HubService.load(Settings(tmp_path, "local", "Hub", "", ""))
    local = MemoryRuntime("local")
    remote = MemoryRuntime("server-a")
    hub.connect_runtime(local, name="Laptop", kind=HostKind.LOCAL)
    hub.connect_runtime(remote, name="Build server")
    user_id = 42
    hub.coordinator.switch_host(user_id, "local")
    await hub.coordinator.create_session(
        user_id, workdir="/home/me/app", name="frontend"
    )
    hub.coordinator.switch_host(user_id, "server-a")
    await hub.coordinator.create_session(
        user_id, workdir="/srv/app", name="backend", backend="codex"
    )
    return (
        TelegramController(hub, allowed_users=frozenset({user_id})),
        hub,
        local,
        remote,
    )


@pytest.mark.asyncio
async def test_host_first_and_global_telegram_flows(tmp_path: Path) -> None:
    controller, hub, _, remote = await configured_controller(tmp_path)
    user_id = 42

    host_screen = await controller.open_sessions(user_id)
    selected_host = await controller.handle_callback(
        user_id, callback("host", entity_token("host", "server-a"))
    )
    settings = await controller.handle_callback(user_id, callback("settings"))
    global_settings = await controller.handle_callback(
        user_id, callback("view", "all")
    )
    global_sessions = await controller.open_sessions(user_id)
    remote_id = next(iter(remote.sessions))
    card = await controller.handle_callback(
        user_id, callback("session", entity_token("session", remote_id))
    )
    updated = await controller.send_text(user_id, "run tests")

    assert host_screen.screen_name == "hosts"
    assert "Build server" in host_screen.text + str(host_screen.keyboard)
    assert selected_host.screen_name == "sessions"
    assert "backend" in str(selected_host.keyboard)
    assert "By server" in str(settings.keyboard)
    assert "All servers" in str(global_settings.keyboard)
    assert "frontend · Laptop" in str(global_sessions.keyboard)
    assert "backend · Build server" in str(global_sessions.keyboard)
    assert card.screen_name == "card" and "server-a:backend" in card.text
    assert updated.session_id == remote_id
    assert remote.sent_text == [(remote_id, "run tests")]
    assert hub.coordinator.session_view_mode(user_id) is SessionViewMode.ALL_HOSTS


@pytest.mark.asyncio
async def test_archive_restore_keeps_creation_time_and_uses_restore_order(
    tmp_path: Path,
) -> None:
    controller, hub, _, remote = await configured_controller(tmp_path)
    user_id = 42
    session_id = next(iter(remote.sessions))
    session = hub.state.sessions[session_id]
    session.created_at = 10.0
    session.live_since_at = 10.0
    token = entity_token("session", session_id)

    confirmation = await controller.handle_callback(
        user_id, callback("archive", token)
    )
    await controller.handle_callback(user_id, callback("archive_do", token))
    archive = await controller.archives(user_id)
    restored = await controller.handle_callback(
        user_id, callback("restore", token)
    )

    assert confirmation.screen_name == "archive_confirmation"
    assert "backend" in str(archive.keyboard)
    assert restored.screen_name == "card"
    assert session.created_at == 10.0
    assert session.live_since_at == session.restored_at
    assert session.live_since_at > 10.0


def test_callback_tokens_are_compact_deterministic_and_resolved_exactly() -> None:
    identifier = "server-with-a-very-long-name-" * 8
    token = entity_token("host", identifier)
    encoded = callback("host", token)

    assert len(encoded.encode()) < 64
    assert resolve_token("host", token, [identifier, "other"]) == identifier
    with pytest.raises(LookupError, match="stale"):
        resolve_token("host", token, ["other"])
