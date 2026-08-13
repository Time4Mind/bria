from __future__ import annotations

import asyncio
import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any, BinaryIO

from .local_registry import LocalSessionRecord
from .provider_binding import TranscriptPathPolicy


@dataclass(frozen=True, slots=True)
class TranscriptCursor:
    offset: int = 0
    file_id: str = ""


@dataclass(frozen=True, slots=True)
class RawTranscriptRecord:
    payload: dict[str, Any]
    start_offset: int
    end_offset: int


@dataclass(frozen=True, slots=True)
class TranscriptPoll:
    records: tuple[RawTranscriptRecord, ...]
    cursor: TranscriptCursor
    reset: bool = False


class JsonlTranscriptSource:
    """Incrementally read complete provider JSONL records without rendering."""

    def __init__(
        self,
        policy: TranscriptPathPolicy,
        *,
        max_records: int = 200,
        max_line_bytes: int = 4 * 1024 * 1024,
    ) -> None:
        if max_records < 1 or max_line_bytes < 1024:
            raise ValueError("transcript polling limits are too small")
        self.policy = policy
        self.max_records = max_records
        self.max_line_bytes = max_line_bytes

    async def poll(
        self,
        session: LocalSessionRecord,
        cursor: TranscriptCursor | None = None,
    ) -> TranscriptPoll:
        if not session.provider_session_id or not session.transcript_path:
            raise ValueError("local session has no bound transcript")
        path = self.policy.validate(
            session.backend,
            session.provider_session_id,
            session.transcript_path,
        )
        selected_cursor = cursor or TranscriptCursor()
        if selected_cursor.offset < 0:
            raise ValueError("transcript offset cannot be negative")
        return await asyncio.to_thread(self._poll_file, path, selected_cursor)

    def _poll_file(self, path: Path, cursor: TranscriptCursor) -> TranscriptPoll:
        with path.open("rb") as handle:
            stat = os.fstat(handle.fileno())
            file_id = f"{stat.st_dev}:{stat.st_ino}"
            reset = bool(cursor.file_id and cursor.file_id != file_id)
            offset = cursor.offset
            if offset > stat.st_size:
                offset = 0
                reset = True
            if reset:
                offset = 0
            offset, repaired = self._repair_offset(handle, offset)
            reset = reset or repaired
            records, next_offset = self._read_records(handle, offset)
        return TranscriptPoll(
            records=tuple(records),
            cursor=TranscriptCursor(offset=next_offset, file_id=file_id),
            reset=reset,
        )

    @staticmethod
    def _repair_offset(handle: BinaryIO, offset: int) -> tuple[int, bool]:
        if offset == 0:
            handle.seek(0)
            return 0, False
        handle.seek(offset - 1)
        if handle.read(1) == b"\n":
            handle.seek(offset)
            return offset, False
        handle.seek(offset)
        handle.readline()
        return handle.tell(), True

    def _read_records(
        self, handle: BinaryIO, offset: int
    ) -> tuple[list[RawTranscriptRecord], int]:
        records: list[RawTranscriptRecord] = []
        safe_offset = offset
        handle.seek(offset)
        while len(records) < self.max_records:
            start = handle.tell()
            line = handle.readline(self.max_line_bytes + 1)
            if not line:
                break
            if len(line) > self.max_line_bytes and not line.endswith(b"\n"):
                raise ValueError("transcript record exceeds configured line limit")
            if not line.endswith(b"\n"):
                handle.seek(start)
                break
            end = handle.tell()
            safe_offset = end
            if not line.strip():
                continue
            try:
                decoded = json.loads(line)
            except (UnicodeDecodeError, json.JSONDecodeError):
                continue
            if not isinstance(decoded, dict):
                continue
            records.append(RawTranscriptRecord(decoded, start, end))
        return records, safe_offset
