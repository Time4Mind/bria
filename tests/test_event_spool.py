from __future__ import annotations

import json
import stat
from pathlib import Path

import pytest

from bria.agent.event_spool import EventSpool, EventSpoolError
from bria.protocol.messages import Event, EventName


def _event(label: str) -> Event:
    return Event(
        EventName.SESSION_CHANGED,
        session_id=label,
        data={"label": label},
    )


def test_spool_persists_sequences_ack_and_compaction(tmp_path: Path) -> None:
    path = tmp_path / "events.json"
    spool = EventSpool(path, host_id="server-a")

    first = spool.append(_event("one"))
    second = spool.append(_event("two"))

    assert [item.sequence for item in spool.pending()] == [1, 2]
    assert first.sequence == 1
    assert second.sequence == 2
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    assert spool.acknowledge(1)
    assert not spool.acknowledge(1)

    restarted = EventSpool(path, host_id="server-a")
    assert restarted.acked_sequence == 1
    assert [item.sequence for item in restarted.pending()] == [2]
    assert restarted.append(_event("three")).sequence == 3

    stored = json.loads(path.read_text(encoding="utf-8"))
    assert [item["sequence"] for item in stored["events"]] == [2, 3]


def test_spool_rejects_unassigned_ack_and_wrong_host(tmp_path: Path) -> None:
    path = tmp_path / "events.json"
    spool = EventSpool(path, host_id="server-a")
    spool.append(_event("one"))

    with pytest.raises(ValueError, match="never assigned"):
        spool.acknowledge(2)
    with pytest.raises(EventSpoolError, match="different host"):
        EventSpool(path, host_id="server-b")


def test_spool_rejects_corrupt_sequence_gap(tmp_path: Path) -> None:
    path = tmp_path / "events.json"
    spool = EventSpool(path, host_id="server-a")
    spool.append(_event("one"))
    data = json.loads(path.read_text(encoding="utf-8"))
    data["events"][0]["sequence"] = 2
    path.write_text(json.dumps(data), encoding="utf-8")

    with pytest.raises(EventSpoolError, match="sequence contains a gap"):
        EventSpool(path, host_id="server-a")
