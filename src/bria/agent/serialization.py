from __future__ import annotations

from typing import Any

from ..runtime.base import CaptureResult, DirectoryEntry, HostHealth, RuntimeSession


def health_payload(health: HostHealth) -> dict[str, Any]:
    return {
        "status": health.status.value,
        "detail": health.detail,
        "version": health.version,
        "capabilities": sorted(item.value for item in health.capabilities),
    }


def session_payload(session: RuntimeSession) -> dict[str, Any]:
    return {
        "session_id": session.session_id,
        "window_id": session.window_id,
        "workdir": session.workdir,
        "name": session.name,
        "backend": session.backend,
        "provider_session_id": session.provider_session_id,
        "state": session.state.value,
    }


def directory_payload(entry: DirectoryEntry) -> dict[str, Any]:
    return {
        "name": entry.name,
        "path": entry.path,
        "modified_at": entry.modified_at,
    }


def capture_payload(result: CaptureResult) -> dict[str, Any]:
    return {
        "text": result.text,
        "ansi": result.ansi,
        "metadata": result.metadata,
    }
