from __future__ import annotations

import asyncio
import shlex
import uuid
from collections.abc import Mapping
from pathlib import Path

from ..domain.enums import LIVE_SESSION_STATES, Capability, HostStatus, SessionState
from .base import (
    CaptureResult,
    CreateSessionRequest,
    DirectoryEntry,
    HostHealth,
    RuntimeSession,
)
from .local_command import CommandRunner, SubprocessRunner
from .local_files import list_child_directories, store_upload
from .local_registry import LocalSessionRecord, LocalSessionRegistry
from .local_tmux import TmuxClient, TmuxWindow
from .local_validation import (
    backend,
    display_name,
    provider_session_id,
    work_directory,
)
from .local_validation import (
    key as validate_key,
)
from .local_validation import (
    session_id as validate_session_id,
)

LOCAL_CAPABILITIES = frozenset(
    {
        Capability.SESSIONS,
        Capability.TRANSCRIPTS,
        Capability.DIRECTORIES,
        Capability.PANE_CAPTURE,
        Capability.INTERACTIVE_KEYS,
        Capability.FILE_UPLOAD,
    }
)
SENSITIVE_ENVIRONMENT = frozenset(
    {
        "ALLOWED_USERS",
        "BRIA_AGENT_TOKEN",
        "OPENAI_API_KEY",
        "TELEGRAM_BOT_TOKEN",
    }
)


class LocalRuntime:
    """A durable HostRuntime implementation backed by one tmux session."""

    def __init__(
        self,
        host_id: str,
        *,
        registry_path: Path | None = None,
        runner: CommandRunner | None = None,
        tmux_session_name: str = "bria",
        backend_commands: Mapping[str, tuple[str, ...]] | None = None,
        hook_environment: Mapping[str, str] | None = None,
        submit_delay: float = 0.5,
    ) -> None:
        self._host_id = validate_session_id(host_id)
        selected_runner = runner or SubprocessRunner()
        self.tmux = TmuxClient(
            selected_runner,
            session_name=tmux_session_name,
            submit_delay=submit_delay,
            sensitive_environment=SENSITIVE_ENVIRONMENT,
        )
        default_path = Path.home() / ".bria" / "runtime" / "local-sessions.json"
        self.registry = LocalSessionRegistry(
            default_path if registry_path is None else registry_path
        )
        self.backend_commands = dict(
            backend_commands or {"claude": ("claude",), "codex": ("codex",)}
        )
        self.hook_environment = {
            "BRIA_HOST_ID": self.host_id,
            **dict(hook_environment or {}),
        }
        self._send_locks: dict[str, asyncio.Lock] = {}

    @property
    def host_id(self) -> str:
        return self._host_id

    async def health(self) -> HostHealth:
        try:
            version = await self.tmux.version()
        except (OSError, RuntimeError, TimeoutError) as exc:
            return HostHealth(status=HostStatus.OFFLINE, detail=str(exc))
        return HostHealth(
            status=HostStatus.ONLINE,
            version=version,
            capabilities=LOCAL_CAPABILITIES,
        )

    async def snapshot(self) -> list[RuntimeSession]:
        windows = await self.tmux.windows()
        by_window_id = {window.window_id: window for window in windows}
        changed = self._reconcile_registered(by_window_id)
        for window in windows:
            if any(
                record.window_id == window.window_id
                for record in self.registry.records.values()
            ):
                continue
            discovered_id = self._discovered_id(window)
            self.registry.records[discovered_id] = self._discovered(
                discovered_id, window
            )
            changed = True
        if changed:
            self.registry.save()
        return [record.runtime_session() for record in self.registry.records.values()]

    async def list_directories(self, path: str) -> list[DirectoryEntry]:
        return await list_child_directories(work_directory(path))

    async def create_session(self, request: CreateSessionRequest) -> RuntimeSession:
        identifier = validate_session_id(request.session_id)
        if identifier in self.registry.records:
            raise ValueError(f"session id already exists: {identifier}")
        selected_backend = backend(request.backend)
        path = work_directory(request.workdir)
        name = display_name(request.name)
        provider_id = provider_session_id(request.resume_provider_session_id)
        if not provider_id and selected_backend == "claude":
            provider_id = str(uuid.uuid4())
        window = await self.tmux.create_window(name, path)
        try:
            is_resume = bool(request.resume_provider_session_id)
            await self.tmux.send_text(
                window.window_id,
                self._launch_command(selected_backend, provider_id, is_resume),
            )
        except Exception:
            await self.tmux.kill_window(window.window_id)
            raise
        record = LocalSessionRecord(
            session_id=identifier,
            window_id=window.window_id,
            workdir=str(path),
            name=window.name,
            backend=selected_backend,
            provider_session_id=provider_id,
        )
        self.registry.upsert(record)
        return record.runtime_session()

    async def send_text(self, session_id: str, text: str) -> None:
        record = self._require_live(session_id)
        async with self._send_lock(record.session_id):
            await self.tmux.send_text(record.window_id, text)

    async def send_key(self, session_id: str, key: str) -> None:
        record = self._require_live(session_id)
        async with self._send_lock(record.session_id):
            await self.tmux.send_key(record.window_id, validate_key(key))

    async def capture_pane(self, session_id: str) -> CaptureResult:
        record = self._require_live(session_id)
        text = await self.tmux.capture(record.window_id)
        return CaptureResult(
            text=text,
            ansi=True,
            metadata={"window_id": record.window_id, "backend": record.backend},
        )

    async def archive_session(self, session_id: str) -> None:
        record = self._require_live(session_id)
        if not record.provider_session_id:
            raise ValueError(
                "session has no provider session id; archiving would not be restorable"
            )
        await self.tmux.kill_window(record.window_id)
        record.window_id = ""
        record.state = SessionState.ARCHIVED
        self.registry.upsert(record)
        self._send_locks.pop(record.session_id, None)

    async def restore_session(self, session_id: str) -> RuntimeSession:
        record = self.registry.get(validate_session_id(session_id))
        if record.state in LIVE_SESSION_STATES:
            raise ValueError("session is already live")
        if not record.provider_session_id:
            raise ValueError("session has no provider session id to resume")
        path = work_directory(record.workdir)
        window = await self.tmux.create_window(display_name(record.name), path)
        try:
            await self.tmux.send_text(
                window.window_id,
                self._launch_command(record.backend, record.provider_session_id, True),
            )
        except Exception:
            await self.tmux.kill_window(window.window_id)
            raise
        record.window_id = window.window_id
        record.name = window.name
        record.state = SessionState.ACTIVE
        self.registry.upsert(record)
        return record.runtime_session()

    async def upload_file(
        self, session_id: str, name: str, content: bytes
    ) -> str:
        record = self._require_live(session_id)
        return await store_upload(work_directory(record.workdir), name, content)

    def _require_live(self, value: str) -> LocalSessionRecord:
        record = self.registry.get(validate_session_id(value))
        if record.state not in LIVE_SESSION_STATES or not record.window_id:
            raise LookupError(f"local session is not live: {value}")
        return record

    def _send_lock(self, identifier: str) -> asyncio.Lock:
        return self._send_locks.setdefault(identifier, asyncio.Lock())

    def _launch_command(
        self, selected_backend: str, provider_id: str, resume: bool
    ) -> str:
        command = list(self.backend_commands.get(selected_backend, ()))
        if not command:
            raise ValueError(f"no command configured for backend: {selected_backend}")
        if selected_backend == "codex" and resume:
            command.extend(("resume", provider_id))
        elif selected_backend == "claude" and resume:
            command.extend(("--resume", provider_id))
        elif selected_backend == "claude" and provider_id:
            command.extend(("--session-id", provider_id))
        environment = (
            "env",
            "BRIA_INTERFACE=telegram",
            f"BRIA_AGENT_BACKEND={selected_backend}",
            *(
                f"{key}={value}"
                for key, value in sorted(self.hook_environment.items())
            ),
        )
        return shlex.join((*environment, *command))

    def _reconcile_registered(self, windows: dict[str, TmuxWindow]) -> bool:
        changed = False
        for record in self.registry.records.values():
            window = windows.get(record.window_id) if record.window_id else None
            if window is None:
                if record.state in LIVE_SESSION_STATES:
                    record.state = SessionState.LOST
                    record.window_id = ""
                    changed = True
                continue
            values = (window.name, window.workdir, SessionState.ACTIVE)
            if (record.name, record.workdir, record.state) != values:
                record.name, record.workdir, record.state = values
                changed = True
        return changed

    def _discovered_id(self, window: TmuxWindow) -> str:
        base = f"tmux-{window.window_id.removeprefix('@')}"
        candidate = base
        suffix = 2
        while candidate in self.registry.records:
            candidate = f"{base}-{suffix}"
            suffix += 1
        return candidate

    @staticmethod
    def _discovered(identifier: str, window: TmuxWindow) -> LocalSessionRecord:
        command = Path(window.command).name
        inferred_backend = "codex" if command == "codex" else "claude"
        return LocalSessionRecord(
            session_id=identifier,
            window_id=window.window_id,
            workdir=window.workdir,
            name=window.name,
            backend=inferred_backend,
        )
