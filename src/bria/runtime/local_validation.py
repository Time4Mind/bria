from __future__ import annotations

import re
from pathlib import Path

_SESSION_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$")
_PROVIDER_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$")
_SAFE_FILE_CHARACTER = re.compile(r"[^A-Za-z0-9._-]+")

SUPPORTED_BACKENDS = frozenset({"claude", "codex"})
SUPPORTED_KEYS = frozenset(
    {
        "BSpace",
        "C-c",
        "C-d",
        "C-u",
        "Down",
        "End",
        "Enter",
        "Escape",
        "Home",
        "Left",
        "NPage",
        "PPage",
        "Right",
        "Space",
        "Tab",
        "Up",
    }
)


def tmux_session_name(value: str) -> str:
    if not _SESSION_ID.fullmatch(value):
        raise ValueError("tmux session name must contain 1-64 safe characters")
    return value


def session_id(value: str) -> str:
    if not _SESSION_ID.fullmatch(value):
        raise ValueError("session_id must contain 1-64 safe characters")
    return value


def provider_session_id(value: str) -> str:
    if value and not _PROVIDER_ID.fullmatch(value):
        raise ValueError("provider session id has an unsupported format")
    return value


def backend(value: str) -> str:
    if value not in SUPPORTED_BACKENDS:
        raise ValueError(f"unsupported backend: {value}")
    return value


def work_directory(value: str) -> Path:
    path = Path(value).expanduser().resolve()
    if not path.is_dir():
        raise ValueError(f"work directory does not exist: {value}")
    return path


def directory(value: str) -> Path:
    return work_directory(value)


def display_name(value: str) -> str:
    name = value.strip()
    if not name or len(name) > 100 or any(ord(char) < 32 for char in name):
        raise ValueError("session name must be 1-100 printable characters")
    return name


def key(value: str) -> str:
    if value not in SUPPORTED_KEYS:
        raise ValueError(f"unsupported interactive key: {value}")
    return value


def upload_name(value: str) -> str:
    normalized = _SAFE_FILE_CHARACTER.sub("_", value.strip().replace(" ", "_"))
    normalized = normalized.strip(".") or "file"
    if len(normalized) <= 80:
        return normalized
    stem, separator, suffix = normalized.rpartition(".")
    if separator and len(suffix) < 20:
        return f"{stem[: 79 - len(suffix)]}.{suffix}"
    return normalized[:80]
