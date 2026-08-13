from __future__ import annotations

import json
import stat
from pathlib import Path

import pytest

from bria.agent.local_event_monitor import LocalEventMonitor
from bria.agent.local_monitor_state import LocalMonitorStateStore
from bria.protocol.messages import Event, EventName
from bria.runtime.base import RuntimeSession
from bria.runtime.local_registry import LocalSessionRecord, LocalSessionRegistry
from bria.runtime.provider_binding import TranscriptPathPolicy
from bria.runtime.transcript_source import JsonlTranscriptSource

PROVIDER_ID = "12345678-1234-4234-8234-123456789abc"


class SnapshotRuntime:
    def __init__(self, registry: LocalSessionRegistry) -> None:
        self.registry = registry

    async def snapshot(self) -> list[RuntimeSession]:
        return [item.runtime_session() for item in self.registry.records.values()]


@pytest.mark.asyncio
async def test_monitor_announces_binding_and_only_new_transcript_activity(
    tmp_path: Path,
) -> None:
    transcript = tmp_path / "claude" / "project" / f"{PROVIDER_ID}.jsonl"
    transcript.parent.mkdir(parents=True)
    transcript.write_text(json.dumps({"type": "assistant"}) + "\n")
    registry = LocalSessionRegistry(tmp_path / "registry.json")
    registry.upsert(
        LocalSessionRecord(
            session_id="session-1",
            window_id="@1",
            workdir=str(tmp_path),
            name="project",
            backend="claude",
            provider_session_id=PROVIDER_ID,
            transcript_path=str(transcript),
        )
    )
    events: list[Event] = []

    async def publish(event: Event) -> object:
        events.append(event)
        return event

    state_path = tmp_path / "monitor.json"
    source = JsonlTranscriptSource(
        TranscriptPathPolicy(
            claude_roots=(tmp_path / "claude",),
            codex_roots=(tmp_path / "codex",),
        )
    )
    monitor = LocalEventMonitor(
        SnapshotRuntime(registry),
        source,
        LocalMonitorStateStore(state_path),
        publish,
        clock=lambda: 50.0,
    )

    await monitor.poll_once()
    await monitor.poll_once()
    with transcript.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps({"type": "user"}) + "\n")
    await monitor.poll_once()

    assert [event.name for event in events] == [
        EventName.SESSION_ANNOUNCED,
        EventName.SESSION_CHANGED,
        EventName.SESSION_OUTPUT,
        EventName.SESSION_OUTPUT,
    ]
    assert events[2].data["record_count"] == 1
    assert events[3].data["event_types"] == ["user"]
    assert stat.S_IMODE(state_path.stat().st_mode) == 0o600

    restarted_events: list[Event] = []

    async def publish_after_restart(event: Event) -> object:
        restarted_events.append(event)
        return event

    restarted = LocalEventMonitor(
        SnapshotRuntime(registry),
        source,
        LocalMonitorStateStore(state_path),
        publish_after_restart,
    )
    await restarted.poll_once()

    assert restarted_events == []
