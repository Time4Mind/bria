from __future__ import annotations

import json
import os
import shlex
import shutil
import sys
import tempfile
from pathlib import Path
from typing import Any

_EVENTS = ("SessionStart", "UserPromptSubmit")


class HookInstaller:
    """Idempotently add Bria lifecycle commands to provider settings."""

    def __init__(
        self,
        *,
        claude_settings: Path | None = None,
        codex_settings: Path | None = None,
        executable: str = "",
    ) -> None:
        home = Path.home()
        self.paths = {
            "claude": claude_settings or home / ".claude" / "settings.json",
            "codex": codex_settings or home / ".codex" / "hooks.json",
        }
        self.executable = executable or _find_executable()

    def install(self, backend: str) -> tuple[Path, int]:
        try:
            path = self.paths[backend]
        except KeyError as exc:
            raise ValueError(f"unsupported hook backend: {backend}") from exc
        settings = self._read(path)
        hooks = settings.setdefault("hooks", {})
        if not isinstance(hooks, dict):
            raise ValueError("provider hooks setting must be an object")
        command = shlex.join(
            (self.executable, "hook", "ingest", "--backend", backend)
        )
        added = 0
        for event in _EVENTS:
            entries = hooks.setdefault(event, [])
            if not isinstance(entries, list):
                raise ValueError(f"provider hook event must be an array: {event}")
            if _contains_command(entries, command):
                continue
            entries.append(
                {
                    "hooks": [
                        {"type": "command", "command": command, "timeout": 5}
                    ]
                }
            )
            added += 1
        if added:
            self._write(path, settings)
        return path, added

    @staticmethod
    def _read(path: Path) -> dict[str, Any]:
        if not path.exists():
            return {}
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise ValueError(f"cannot read provider settings: {path}") from exc
        if not isinstance(value, dict):
            raise ValueError("provider settings must contain an object")
        return value

    @staticmethod
    def _write(path: Path, settings: dict[str, Any]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        mode = path.stat().st_mode & 0o777 if path.exists() else 0o600
        fd, temporary_name = tempfile.mkstemp(
            dir=path.parent, prefix=f".{path.name}.", suffix=".tmp"
        )
        temporary = Path(temporary_name)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                json.dump(settings, handle, ensure_ascii=False, indent=2)
                handle.write("\n")
                handle.flush()
                os.fsync(handle.fileno())
            temporary.chmod(mode)
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)


def _contains_command(entries: list[object], command: str) -> bool:
    for entry in entries:
        if not isinstance(entry, dict):
            continue
        nested = entry.get("hooks", [])
        if isinstance(nested, list) and any(
            isinstance(item, dict) and item.get("command") == command
            for item in nested
        ):
            return True
    return False


def _find_executable() -> str:
    discovered = shutil.which("bria")
    if discovered:
        return discovered
    sibling = Path(sys.executable).with_name("bria")
    return str(sibling) if sibling.exists() else "bria"
