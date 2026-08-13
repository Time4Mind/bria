from __future__ import annotations

import json

import pytest

from bria.runtime.local_registry import LocalSessionRecord
from bria.runtime.provider_binding import TranscriptPathPolicy
from bria.runtime.transcript_source import JsonlTranscriptSource, TranscriptCursor

PROVIDER_ID = "12345678-1234-4234-8234-123456789abc"


def source_and_session(tmp_path):
    root = tmp_path / "claude"
    path = root / "project" / f"{PROVIDER_ID}.jsonl"
    path.parent.mkdir(parents=True)
    policy = TranscriptPathPolicy(
        claude_roots=(root,), codex_roots=(tmp_path / "codex",)
    )
    session = LocalSessionRecord(
        session_id="local-1",
        window_id="@1",
        workdir=str(tmp_path),
        name="project",
        backend="claude",
        provider_session_id=PROVIDER_ID,
        transcript_path=str(path),
    )
    return JsonlTranscriptSource(policy), session, path


@pytest.mark.asyncio
async def test_poll_returns_only_complete_new_records_and_byte_cursor(tmp_path) -> None:
    source, session, path = source_and_session(tmp_path)
    first_line = json.dumps({"type": "first"}).encode() + b"\n"
    path.write_bytes(first_line + b'{"type":"partial"')

    first = await source.poll(session)
    with path.open("ab") as handle:
        handle.write(b"}\n")
    second = await source.poll(session, first.cursor)
    third = await source.poll(session, second.cursor)

    assert [item.payload["type"] for item in first.records] == ["first"]
    assert first.cursor.offset == len(first_line)
    assert [item.payload["type"] for item in second.records] == ["partial"]
    assert second.cursor.offset == path.stat().st_size
    assert third.records == ()
    assert third.cursor == second.cursor


@pytest.mark.asyncio
async def test_poll_resets_after_truncation_and_skips_malformed_lines(tmp_path) -> None:
    source, session, path = source_and_session(tmp_path)
    path.write_text('{"type":"old","padding":"xxxxxxxx"}\n', encoding="utf-8")
    old = await source.poll(session)
    path.write_text('not-json\n{"type":"new"}\n', encoding="utf-8")

    updated = await source.poll(session, old.cursor)

    assert updated.reset
    assert [item.payload["type"] for item in updated.records] == ["new"]
    assert updated.cursor.offset == path.stat().st_size


@pytest.mark.asyncio
async def test_poll_repairs_cursor_that_points_inside_a_line(tmp_path) -> None:
    source, session, path = source_and_session(tmp_path)
    path.write_text('{"type":"first"}\n{"type":"second"}\n', encoding="utf-8")

    result = await source.poll(session, TranscriptCursor(offset=3))

    assert result.reset
    assert [item.payload["type"] for item in result.records] == ["second"]
