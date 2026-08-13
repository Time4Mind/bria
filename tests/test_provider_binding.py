from __future__ import annotations

import json
from pathlib import Path

import pytest

from bria.runtime.local_registry import LocalSessionRecord, LocalSessionRegistry
from bria.runtime.provider_binding import HookBindingService, TranscriptPathPolicy

CLAUDE_ID = "12345678-1234-4234-8234-123456789abc"
CODEX_ID = "87654321-4321-4321-8321-cba987654321"


def registry_with_session(
    path: Path, *, backend: str = "claude"
) -> LocalSessionRegistry:
    registry = LocalSessionRegistry(path)
    registry.upsert(
        LocalSessionRecord(
            session_id="local-1",
            window_id="@7",
            workdir="/work/project",
            name="project",
            backend=backend,
        )
    )
    return registry


def policy(tmp_path: Path) -> TranscriptPathPolicy:
    return TranscriptPathPolicy(
        claude_roots=(tmp_path / "claude",),
        codex_roots=(tmp_path / "codex",),
    )


def test_hook_binds_claude_transcript_by_local_session_id(tmp_path) -> None:
    registry_path = tmp_path / "registry.json"
    registry = registry_with_session(registry_path)
    transcript = tmp_path / "claude" / "project" / f"{CLAUDE_ID}.jsonl"
    transcript.parent.mkdir(parents=True)
    transcript.write_text('{"type":"system"}\n', encoding="utf-8")

    binding = HookBindingService(registry, policy(tmp_path)).bind(
        session_id="local-1",
        backend="claude",
        provider_session_id=CLAUDE_ID,
        transcript_path=transcript,
    )
    reloaded = LocalSessionRegistry(registry_path).get("local-1")

    assert binding.provider_session_id == CLAUDE_ID
    assert binding.transcript_path == str(transcript.resolve())
    assert binding.event_data() == {
        "window_id": "@7",
        "backend": "claude",
        "provider_session_id": CLAUDE_ID,
        "transcript_path": str(transcript.resolve()),
    }
    assert reloaded.provider_session_id == CLAUDE_ID
    assert reloaded.transcript_path == str(transcript.resolve())
    assert registry_path.stat().st_mode & 0o777 == 0o600
    assert registry_path.with_suffix(".json.lock").stat().st_mode & 0o777 == 0o600


def test_hook_binds_codex_rollout_by_tmux_window(tmp_path) -> None:
    registry = registry_with_session(tmp_path / "registry.json", backend="codex")
    transcript = tmp_path / "codex" / "2026" / "08" / "09" / "rollout.jsonl"
    transcript.parent.mkdir(parents=True)
    transcript.write_text(
        json.dumps({"type": "session_meta", "payload": {"id": CODEX_ID}}) + "\n",
        encoding="utf-8",
    )

    binding = HookBindingService(registry, policy(tmp_path)).bind(
        window_id="@7",
        backend="codex",
        provider_session_id=CODEX_ID,
        transcript_path=transcript,
    )

    assert binding.session_id == "local-1"
    assert binding.window_id == "@7"
    assert registry.get("local-1").provider_session_id == CODEX_ID


def test_binding_rejects_untrusted_or_mismatched_data(tmp_path) -> None:
    registry = registry_with_session(tmp_path / "registry.json")
    outside = tmp_path / "outside" / f"{CLAUDE_ID}.jsonl"
    outside.parent.mkdir()
    outside.write_text("{}\n", encoding="utf-8")
    service = HookBindingService(registry, policy(tmp_path))

    with pytest.raises(ValueError, match="outside configured"):
        service.bind(
            session_id="local-1",
            backend="claude",
            provider_session_id=CLAUDE_ID,
            transcript_path=outside,
        )
    with pytest.raises(ValueError, match="exactly one"):
        service.bind(
            session_id="local-1",
            window_id="@7",
            backend="claude",
            provider_session_id=CLAUDE_ID,
            transcript_path=outside,
        )
    with pytest.raises(ValueError, match="UUID"):
        service.bind(
            window_id="@7",
            backend="claude",
            provider_session_id="../../not-an-id",
            transcript_path=outside,
        )


def test_runtime_save_preserves_binding_written_by_hook_process(tmp_path) -> None:
    registry_path = tmp_path / "registry.json"
    runtime_registry = registry_with_session(registry_path)
    hook_registry = LocalSessionRegistry(registry_path)
    transcript = tmp_path / "claude" / "project" / f"{CLAUDE_ID}.jsonl"
    transcript.parent.mkdir(parents=True)
    transcript.write_text("{}\n", encoding="utf-8")
    HookBindingService(hook_registry, policy(tmp_path)).bind(
        window_id="@7",
        backend="claude",
        provider_session_id=CLAUDE_ID,
        transcript_path=transcript,
    )

    stale = runtime_registry.get("local-1")
    stale.name = "renamed"
    runtime_registry.upsert(stale)
    reloaded = LocalSessionRegistry(registry_path).get("local-1")

    assert reloaded.name == "renamed"
    assert reloaded.provider_session_id == CLAUDE_ID
    assert reloaded.transcript_path == str(transcript.resolve())


def test_provider_binding_cannot_be_attached_to_two_local_sessions(tmp_path) -> None:
    registry_path = tmp_path / "registry.json"
    registry = registry_with_session(registry_path)
    registry.upsert(
        LocalSessionRecord(
            session_id="local-2",
            window_id="@8",
            workdir="/work/other",
            name="other",
            backend="claude",
        )
    )
    transcript = tmp_path / "claude" / "project" / f"{CLAUDE_ID}.jsonl"
    transcript.parent.mkdir(parents=True)
    transcript.write_text("{}\n", encoding="utf-8")
    service = HookBindingService(registry, policy(tmp_path))
    service.bind(
        session_id="local-1",
        backend="claude",
        provider_session_id=CLAUDE_ID,
        transcript_path=transcript,
    )

    with pytest.raises(ValueError, match="already bound"):
        service.bind(
            session_id="local-2",
            backend="claude",
            provider_session_id=CLAUDE_ID,
            transcript_path=transcript,
        )
