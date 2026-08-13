from __future__ import annotations

import asyncio
import uuid
from dataclasses import dataclass
from pathlib import Path

from .local_command import CommandResult, CommandRunner, LocalCommandError
from .local_validation import tmux_session_name

_FIELD_SEPARATOR = "\x1f"


@dataclass(frozen=True, slots=True)
class TmuxWindow:
    window_id: str
    name: str
    workdir: str
    command: str


class TmuxClient:
    """Narrow tmux CLI adapter; no libtmux or shell process is involved."""

    def __init__(
        self,
        runner: CommandRunner,
        *,
        session_name: str = "bria",
        main_window_name: str = "main",
        submit_delay: float = 0.5,
        sensitive_environment: frozenset[str] = frozenset(),
    ) -> None:
        self.runner = runner
        self.session_name = tmux_session_name(session_name)
        self.main_window_name = main_window_name
        self.submit_delay = submit_delay
        self.sensitive_environment = sensitive_environment

    async def version(self) -> str:
        result = await self.runner.run(("tmux", "-V"))
        self._require(("tmux", "-V"), result)
        return result.stdout.strip()

    async def has_session(self) -> bool:
        result = await self.runner.run(
            ("tmux", "has-session", "-t", f"={self.session_name}")
        )
        return result.returncode == 0

    async def ensure_session(self, start_directory: Path) -> None:
        if not await self.has_session():
            argv = (
                "tmux",
                "new-session",
                "-d",
                "-s",
                self.session_name,
                "-n",
                self.main_window_name,
                "-c",
                str(start_directory),
            )
            result = await self.runner.run(argv)
            if result.returncode != 0 and not await self.has_session():
                self._require(argv, result)
        await self._scrub_environment()

    async def windows(self) -> list[TmuxWindow]:
        if not await self.has_session():
            return []
        row_format = _FIELD_SEPARATOR.join(
            (
                "#{window_id}",
                "#{window_name}",
                "#{pane_current_path}",
                "#{pane_current_command}",
            )
        )
        argv = (
            "tmux",
            "list-windows",
            "-t",
            f"={self.session_name}",
            "-F",
            row_format,
        )
        result = await self.runner.run(argv)
        self._require(argv, result)
        windows: list[TmuxWindow] = []
        for row in result.stdout.splitlines():
            fields = row.split(_FIELD_SEPARATOR)
            if len(fields) != 4 or fields[1] == self.main_window_name:
                continue
            windows.append(TmuxWindow(*fields))
        return windows

    async def create_window(self, name: str, workdir: Path) -> TmuxWindow:
        await self.ensure_session(workdir)
        final_name = await self._unique_name(name)
        argv = (
            "tmux",
            "new-window",
            "-d",
            "-P",
            "-F",
            _FIELD_SEPARATOR.join(("#{window_id}", "#{window_name}")),
            "-t",
            f"={self.session_name}:",
            "-n",
            final_name,
            "-c",
            str(workdir),
        )
        result = await self.runner.run(argv)
        self._require(argv, result)
        fields = result.stdout.strip().split(_FIELD_SEPARATOR)
        if len(fields) != 2 or not fields[0].startswith("@"):
            raise RuntimeError("tmux returned an invalid new-window response")
        window = TmuxWindow(fields[0], fields[1], str(workdir), "")
        option_argv = (
            "tmux",
            "set-window-option",
            "-t",
            self.target(window.window_id),
            "allow-rename",
            "off",
        )
        option_result = await self.runner.run(option_argv)
        self._require(option_argv, option_result)
        return window

    async def send_text(self, window_id: str, text: str) -> None:
        buffer_name = f"bria-{uuid.uuid4().hex}"
        load_argv = ("tmux", "load-buffer", "-b", buffer_name, "-")
        loaded = await self.runner.run(load_argv, input_bytes=text.encode("utf-8"))
        self._require(load_argv, loaded)
        paste_argv = (
            "tmux",
            "paste-buffer",
            "-d",
            "-b",
            buffer_name,
            "-t",
            self.target(window_id),
        )
        pasted = await self.runner.run(paste_argv)
        self._require(paste_argv, pasted)
        if self.submit_delay:
            await asyncio.sleep(self.submit_delay)
        await self.send_key(window_id, "Enter")

    async def send_key(self, window_id: str, key: str) -> None:
        argv = ("tmux", "send-keys", "-t", self.target(window_id), key)
        result = await self.runner.run(argv)
        self._require(argv, result)

    async def capture(self, window_id: str) -> str:
        argv = (
            "tmux",
            "capture-pane",
            "-e",
            "-p",
            "-t",
            self.target(window_id),
        )
        result = await self.runner.run(argv)
        self._require(argv, result)
        return result.stdout

    async def kill_window(self, window_id: str) -> None:
        argv = ("tmux", "kill-window", "-t", self.target(window_id))
        result = await self.runner.run(argv)
        self._require(argv, result)

    def target(self, window_id: str) -> str:
        return f"={self.session_name}:{window_id}"

    async def _unique_name(self, requested: str) -> str:
        existing = {window.name for window in await self.windows()}
        if requested not in existing:
            return requested
        suffix = 2
        while f"{requested}-{suffix}" in existing:
            suffix += 1
        return f"{requested}-{suffix}"

    async def _scrub_environment(self) -> None:
        for variable in sorted(self.sensitive_environment):
            await self.runner.run(
                (
                    "tmux",
                    "set-environment",
                    "-u",
                    "-t",
                    f"={self.session_name}",
                    variable,
                )
            )

    @staticmethod
    def _require(argv: tuple[str, ...], result: CommandResult) -> None:
        if result.returncode != 0:
            raise LocalCommandError(argv, result)
