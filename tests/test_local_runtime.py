from __future__ import annotations

import os
from pathlib import Path

import pytest

from bria.domain.enums import HostStatus, SessionState
from bria.runtime.base import CreateSessionRequest
from bria.runtime.local_command import CommandResult
from bria.runtime.local_runtime import LocalRuntime


class FakeTmuxRunner:
    def __init__(self) -> None:
        self.session_exists = False
        self.windows: dict[str, tuple[str, str, str]] = {}
        self.buffers: dict[str, bytes] = {}
        self.pasted: list[tuple[str, bytes]] = []
        self.keys: list[tuple[str, str]] = []
        self.calls: list[tuple[tuple[str, ...], bytes | None]] = []
        self.next_window = 1

    async def run(
        self, argv: tuple[str, ...], *, input_bytes: bytes | None = None
    ) -> CommandResult:
        self.calls.append((argv, input_bytes))
        operation = argv[1]
        if operation == "-V":
            return CommandResult(0, "tmux 3.4\n")
        if operation == "has-session":
            return CommandResult(0 if self.session_exists else 1)
        if operation == "new-session":
            self.session_exists = True
            return CommandResult(0)
        if operation == "list-windows":
            separator = "\x1f"
            rows = [separator.join(("@0", "main", "/tmp", "zsh"))]
            rows.extend(
                separator.join((identifier, name, workdir, command))
                for identifier, (name, workdir, command) in self.windows.items()
            )
            return CommandResult(0, "\n".join(rows))
        if operation == "new-window":
            identifier = f"@{self.next_window}"
            self.next_window += 1
            name = argv[argv.index("-n") + 1]
            workdir = argv[argv.index("-c") + 1]
            self.windows[identifier] = (name, workdir, "zsh")
            return CommandResult(0, f"{identifier}\x1f{name}\n")
        if operation == "set-window-option":
            return CommandResult(0)
        if operation == "set-environment":
            return CommandResult(0)
        if operation == "load-buffer":
            self.buffers[argv[argv.index("-b") + 1]] = input_bytes or b""
            return CommandResult(0)
        if operation == "paste-buffer":
            name = argv[argv.index("-b") + 1]
            target = argv[argv.index("-t") + 1]
            identifier = target.rsplit(":", maxsplit=1)[1]
            content = self.buffers.pop(name)
            self.pasted.append((identifier, content))
            if b"BRIA_AGENT_BACKEND=codex" in content:
                window_name, workdir, _ = self.windows[identifier]
                self.windows[identifier] = (window_name, workdir, "codex")
            elif b"BRIA_AGENT_BACKEND=claude" in content:
                window_name, workdir, _ = self.windows[identifier]
                self.windows[identifier] = (window_name, workdir, "claude")
            return CommandResult(0)
        if operation == "send-keys":
            target = argv[argv.index("-t") + 1]
            self.keys.append((target.rsplit(":", 1)[1], argv[-1]))
            return CommandResult(0)
        if operation == "capture-pane":
            return CommandResult(0, "\x1b[32mready\x1b[0m\n")
        if operation == "kill-window":
            target = argv[argv.index("-t") + 1]
            self.windows.pop(target.rsplit(":", 1)[1])
            return CommandResult(0)
        raise AssertionError(f"unexpected tmux command: {argv}")


def runtime(tmp_path: Path, runner: FakeTmuxRunner) -> LocalRuntime:
    return LocalRuntime(
        "local",
        registry_path=tmp_path / "registry.json",
        runner=runner,
        submit_delay=0,
    )


@pytest.mark.asyncio
async def test_local_runtime_full_session_lifecycle(tmp_path) -> None:
    runner = FakeTmuxRunner()
    selected = runtime(tmp_path, runner)
    workdir = tmp_path / "project"
    workdir.mkdir()

    health = await selected.health()
    created = await selected.create_session(
        CreateSessionRequest("session-1", str(workdir), "project")
    )
    await selected.send_text(created.session_id, "hello 'quoted' мир")
    await selected.send_key(created.session_id, "Escape")
    capture = await selected.capture_pane(created.session_id)
    uploaded = await selected.upload_file(
        created.session_id, "../../unsafe name.txt", b"payload"
    )

    assert health.status is HostStatus.ONLINE
    assert health.version == "tmux 3.4"
    assert created.provider_session_id
    launch = runner.pasted[0][1].decode()
    assert "claude --session-id" in launch
    assert "BRIA_HOST_ID=local" in launch
    assert runner.pasted[1][1] == "hello 'quoted' мир".encode()
    assert capture.ansi and "ready" in capture.text
    assert uploaded.startswith(".bria-inbox/")
    assert (workdir / uploaded).read_bytes() == b"payload"
    assert not uploaded.startswith("..")

    await selected.archive_session(created.session_id)
    restored = await selected.restore_session(created.session_id)

    assert restored.state is SessionState.ACTIVE
    assert restored.window_id != created.window_id
    assert b"--resume" in runner.pasted[-1][1]
    assert (tmp_path / "registry.json").stat().st_mode & 0o777 == 0o600


@pytest.mark.asyncio
async def test_snapshot_reconciles_missing_and_unregistered_windows(tmp_path) -> None:
    runner = FakeTmuxRunner()
    selected = runtime(tmp_path, runner)
    workdir = tmp_path / "project"
    workdir.mkdir()
    created = await selected.create_session(
        CreateSessionRequest("known", str(workdir), "known", backend="codex")
    )
    runner.windows.pop(created.window_id)
    runner.windows["@9"] = ("outside", str(workdir), "codex")

    sessions = {item.session_id: item for item in await selected.snapshot()}

    assert sessions["known"].state is SessionState.LOST
    assert sessions["known"].window_id == ""
    assert sessions["tmux-9"].state is SessionState.ACTIVE
    assert sessions["tmux-9"].backend == "codex"


@pytest.mark.asyncio
async def test_directory_listing_uses_modification_recency(tmp_path) -> None:
    runner = FakeTmuxRunner()
    selected = runtime(tmp_path, runner)
    older = tmp_path / "older"
    newer = tmp_path / "newer"
    older.mkdir()
    newer.mkdir()
    os.utime(older, (10, 10))
    os.utime(newer, (20, 20))

    entries = await selected.list_directories(str(tmp_path))

    assert [entry.name for entry in entries] == ["newer", "older"]


@pytest.mark.asyncio
async def test_rejects_unsafe_runtime_inputs(tmp_path) -> None:
    runner = FakeTmuxRunner()
    selected = runtime(tmp_path, runner)

    with pytest.raises(ValueError, match="session_id"):
        await selected.create_session(
            CreateSessionRequest("../../bad", str(tmp_path), "bad")
        )
    with pytest.raises(ValueError, match="backend"):
        await selected.create_session(
            CreateSessionRequest("safe", str(tmp_path), "safe", backend="shell")
        )


@pytest.mark.asyncio
async def test_codex_without_rollout_id_cannot_be_archived(tmp_path) -> None:
    runner = FakeTmuxRunner()
    selected = runtime(tmp_path, runner)
    created = await selected.create_session(
        CreateSessionRequest("codex-new", str(tmp_path), "codex", backend="codex")
    )

    with pytest.raises(ValueError, match="would not be restorable"):
        await selected.archive_session(created.session_id)

    assert created.window_id in runner.windows
