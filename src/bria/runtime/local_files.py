from __future__ import annotations

import asyncio
import os
import tempfile
import time
from pathlib import Path

from .base import DirectoryEntry
from .local_validation import upload_name


async def list_child_directories(path: Path) -> list[DirectoryEntry]:
    return await asyncio.to_thread(_list_child_directories, path)


def _list_child_directories(path: Path) -> list[DirectoryEntry]:
    entries: list[DirectoryEntry] = []
    try:
        children = list(path.iterdir())
    except OSError as exc:
        raise RuntimeError(f"cannot list directory {path}: {exc}") from exc
    for child in children:
        try:
            if not child.is_dir():
                continue
            entries.append(
                DirectoryEntry(
                    name=child.name,
                    path=str(child.resolve()),
                    modified_at=child.stat().st_mtime,
                )
            )
        except OSError:
            continue
    return sorted(entries, key=lambda entry: (-entry.modified_at, entry.name.lower()))


async def store_upload(workdir: Path, name: str, content: bytes) -> str:
    return await asyncio.to_thread(_store_upload, workdir, name, content)


def _store_upload(workdir: Path, name: str, content: bytes) -> str:
    safe_name = upload_name(name)
    inbox = workdir / ".bria-inbox"
    inbox.mkdir(mode=0o700, parents=True, exist_ok=True)
    resolved_inbox = inbox.resolve()
    if not resolved_inbox.is_relative_to(workdir):
        raise RuntimeError("session inbox resolves outside its work directory")
    target = resolved_inbox / f"{time.time_ns()}-{safe_name}"
    fd, temporary_name = tempfile.mkstemp(dir=resolved_inbox, prefix=".upload-")
    temporary = Path(temporary_name)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        temporary.chmod(0o600)
        temporary.replace(target)
    finally:
        temporary.unlink(missing_ok=True)
    return str(target.relative_to(workdir))
