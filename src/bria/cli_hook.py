from __future__ import annotations

import json
import logging
import os
import subprocess
import sys
from collections.abc import Mapping
from pathlib import Path
from typing import IO

from .config import Settings
from .runtime.local_registry import LocalSessionRegistry
from .runtime.provider_binding import HookBindingService, TranscriptPathPolicy

logger = logging.getLogger(__name__)


def bind_provider(
    settings: Settings,
    *,
    backend: str,
    provider_session_id: str,
    transcript_path: Path,
    session_id: str = "",
    window_id: str = "",
) -> int:
    binding = _service(settings).bind(
        backend=backend,
        provider_session_id=provider_session_id,
        transcript_path=transcript_path,
        session_id=session_id,
        window_id=window_id,
    )
    print(json.dumps(binding.event_data(), ensure_ascii=False, sort_keys=True))
    return 0


def ingest_hook(
    settings: Settings,
    *,
    backend: str,
    source: IO[str] = sys.stdin,
    environment: Mapping[str, str] = os.environ,
) -> int:
    """Consume one provider hook without ever blocking the user's prompt."""
    try:
        payload = json.load(source)
        if not isinstance(payload, dict):
            raise ValueError("hook payload must be an object")
        pane_id = environment.get("TMUX_PANE", "")
        if not pane_id:
            raise ValueError("TMUX_PANE is not set")
        transcript_path = str(payload.get("transcript_path") or "")
        if not transcript_path:
            raise ValueError("hook payload has no transcript_path")
        _service(settings).bind(
            backend=backend,
            provider_session_id=str(payload.get("session_id") or ""),
            transcript_path=transcript_path,
            window_id=_window_id_for_pane(pane_id),
        )
    except Exception as exc:
        logger.warning("provider hook was ignored: %s", exc)
    return 0


def _service(settings: Settings) -> HookBindingService:
    registry = LocalSessionRegistry(
        settings.runtime_registry_file(settings.host_id)
    )
    return HookBindingService(registry, TranscriptPathPolicy.defaults())


def _window_id_for_pane(pane_id: str) -> str:
    completed = subprocess.run(
        ["tmux", "display-message", "-t", pane_id, "-p", "#{window_id}"],
        capture_output=True,
        check=False,
        text=True,
        timeout=3,
    )
    if completed.returncode != 0:
        detail = completed.stderr.strip() or "tmux lookup failed"
        raise RuntimeError(detail)
    return completed.stdout.strip()
