from __future__ import annotations

import pytest

from bria.config import Settings


def test_settings_parse_runtime_and_network_environment(monkeypatch, tmp_path) -> None:
    monkeypatch.setenv("BRIA_DATA_DIR", str(tmp_path))
    monkeypatch.setenv("BRIA_HUB_PORT", "9443")
    monkeypatch.setenv("BRIA_LOCAL_RUNTIME", "no")
    monkeypatch.setenv(
        "BRIA_CLAUDE_COMMAND", "claude --dangerously-skip-permissions"
    )
    monkeypatch.setenv("BRIA_EVENT_POLL_INTERVAL", "0.25")
    monkeypatch.setenv("BRIA_ALLOWED_USERS", "10, 20")

    settings = Settings.from_env()

    assert settings.hub_port == 9443
    assert not settings.local_runtime_enabled
    assert settings.claude_command == (
        "claude",
        "--dangerously-skip-permissions",
    )
    assert settings.event_poll_interval == 0.25
    assert settings.allowed_users == frozenset({10, 20})
    assert settings.runtime_registry_file("server-a") == (
        tmp_path / "runtime" / "server-a-sessions.json"
    )
    assert settings.event_monitor_file("server-a") == (
        tmp_path / "runtime" / "server-a-monitor.json"
    )


@pytest.mark.parametrize(
    ("name", "value", "message"),
    [
        ("BRIA_HUB_PORT", "70000", "between 0 and 65535"),
        ("BRIA_LOCAL_RUNTIME", "perhaps", "invalid boolean"),
        ("BRIA_EVENT_POLL_INTERVAL", "0", "greater than zero"),
        ("BRIA_ALLOWED_USERS", "1,nope", "comma-separated integers"),
    ],
)
def test_settings_reject_invalid_environment(
    monkeypatch, name: str, value: str, message: str
) -> None:
    monkeypatch.setenv(name, value)

    with pytest.raises(ValueError, match=message):
        Settings.from_env()
