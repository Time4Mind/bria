from __future__ import annotations

import os
import shlex
import socket
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True, slots=True)
class Settings:
    data_dir: Path
    host_id: str
    host_name: str
    hub_url: str
    agent_token: str
    telegram_token: str = ""
    allowed_users: frozenset[int] = frozenset()
    hub_bind_host: str = "127.0.0.1"
    hub_port: int = 8765
    local_runtime_enabled: bool = True
    tmux_session_name: str = "bria"
    claude_command: tuple[str, ...] = ("claude",)
    codex_command: tuple[str, ...] = ("codex",)
    event_poll_interval: float = 1.0
    tls_cert_file: Path | None = None
    tls_key_file: Path | None = None
    tls_ca_file: Path | None = None

    @classmethod
    def from_env(cls) -> Settings:
        data_dir = Path(
            _env("DATA_DIR", "~/.bria")
        ).expanduser()
        return cls(
            data_dir=data_dir,
            host_id=_env("HOST_ID", "local").strip() or "local",
            host_name=_env("HOST_NAME", "").strip()
            or socket.gethostname(),
            hub_url=_env("HUB_URL", "").strip(),
            agent_token=_env("AGENT_TOKEN", "").strip(),
            telegram_token=_env("TELEGRAM_TOKEN", "").strip(),
            allowed_users=_user_ids(
                _env("ALLOWED_USERS", "")
            ),
            hub_bind_host=_env("HUB_BIND", "127.0.0.1").strip()
            or "127.0.0.1",
            hub_port=_port(_env("HUB_PORT", "8765")),
            local_runtime_enabled=_boolean(
                _env("LOCAL_RUNTIME", "true")
            ),
            tmux_session_name=_env("TMUX_SESSION", "bria").strip() or "bria",
            claude_command=_command(
                _env("CLAUDE_COMMAND", "claude")
            ),
            codex_command=_command(
                _env("CODEX_COMMAND", "codex")
            ),
            event_poll_interval=_positive_float(
                _env("EVENT_POLL_INTERVAL", "1.0"),
                name="BRIA_EVENT_POLL_INTERVAL",
            ),
            tls_cert_file=_optional_path(_env("TLS_CERT", "")),
            tls_key_file=_optional_path(_env("TLS_KEY", "")),
            tls_ca_file=_optional_path(_env("TLS_CA", "")),
        )

    @property
    def state_file(self) -> Path:
        return self.data_dir / "state.json"

    @property
    def agent_tokens_file(self) -> Path:
        return self.data_dir / "agent-tokens.json"

    def runtime_registry_file(self, host_id: str) -> Path:
        return self.data_dir / "runtime" / f"{host_id}-sessions.json"

    def event_spool_file(self, host_id: str) -> Path:
        return self.data_dir / "runtime" / f"{host_id}-events.json"

    def event_monitor_file(self, host_id: str) -> Path:
        return self.data_dir / "runtime" / f"{host_id}-monitor.json"


def _env(name: str, default: str) -> str:
    return os.getenv(f"BRIA_{name}", default)


def _port(value: str) -> int:
    try:
        port = int(value)
    except ValueError as exc:
        raise ValueError("BRIA_HUB_PORT must be an integer") from exc
    if not 0 <= port <= 65535:
        raise ValueError("BRIA_HUB_PORT must be between 0 and 65535")
    return port


def _boolean(value: str) -> bool:
    normalized = value.strip().lower()
    if normalized in {"1", "true", "yes", "on"}:
        return True
    if normalized in {"0", "false", "no", "off"}:
        return False
    raise ValueError(f"invalid boolean value: {value}")


def _command(value: str) -> tuple[str, ...]:
    command = tuple(shlex.split(value))
    if not command:
        raise ValueError("agent command must not be empty")
    return command


def _optional_path(value: str) -> Path | None:
    return Path(value).expanduser() if value.strip() else None


def _positive_float(value: str, *, name: str) -> float:
    try:
        number = float(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be a number") from exc
    if number <= 0:
        raise ValueError(f"{name} must be greater than zero")
    return number


def _user_ids(value: str) -> frozenset[int]:
    if not value.strip():
        return frozenset()
    try:
        identifiers = frozenset(
            int(item.strip()) for item in value.split(",") if item.strip()
        )
    except ValueError as exc:
        raise ValueError(
            "BRIA_ALLOWED_USERS must be comma-separated integers"
        ) from exc
    if any(identifier <= 0 for identifier in identifiers):
        raise ValueError("BRIA_ALLOWED_USERS must contain positive integers")
    return identifiers
