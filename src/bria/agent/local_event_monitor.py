from __future__ import annotations

import asyncio
import json
import logging
import time
from collections.abc import Awaitable, Callable
from typing import Protocol

from ..protocol.messages import Event, EventName
from ..runtime.base import RuntimeSession
from ..runtime.local_registry import LocalSessionRecord, LocalSessionRegistry
from ..runtime.transcript_source import (
    JsonlTranscriptSource,
    RawTranscriptRecord,
    TranscriptCursor,
)
from .local_monitor_state import LocalMonitorStateStore

logger = logging.getLogger(__name__)

EventPublisher = Callable[[Event], Awaitable[object]]


class MonitorRuntime(Protocol):
    registry: LocalSessionRegistry

    async def snapshot(self) -> list[RuntimeSession]: ...


class LocalEventMonitor:
    """Reconcile local runtime metadata and turn JSONL activity into events."""

    def __init__(
        self,
        runtime: MonitorRuntime,
        transcript_source: JsonlTranscriptSource,
        state_store: LocalMonitorStateStore,
        publisher: EventPublisher,
        *,
        poll_interval: float = 1.0,
        clock: Callable[[], float] = time.time,
    ) -> None:
        if poll_interval <= 0:
            raise ValueError("poll_interval must be greater than zero")
        self.runtime = runtime
        self.transcript_source = transcript_source
        self.state_store = state_store
        self.publisher = publisher
        self.poll_interval = poll_interval
        self.clock = clock

    async def run(self) -> None:
        while True:
            try:
                await self.poll_once()
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception("local event monitor poll failed")
            await asyncio.sleep(self.poll_interval)

    async def poll_once(self) -> None:
        await self.runtime.snapshot()
        self.runtime.registry.refresh()
        for record in sorted(
            self.runtime.registry.records.values(), key=lambda item: item.session_id
        ):
            await self._poll_session(record)

    async def _poll_session(self, record: LocalSessionRecord) -> None:
        position = self.state_store.get(record.session_id)
        runtime_signature = _runtime_signature(record)
        if runtime_signature != position.runtime_signature:
            name = (
                EventName.SESSION_ANNOUNCED
                if not position.runtime_signature
                else EventName.SESSION_CHANGED
            )
            await self.publisher(
                Event(
                    name,
                    session_id=record.session_id,
                    data=_runtime_data(record, self.clock()),
                )
            )
            position.runtime_signature = runtime_signature
            self.state_store.save()

        if not record.provider_session_id or not record.transcript_path:
            return
        binding_signature = _binding_signature(record)
        if binding_signature != position.binding_signature:
            await self.publisher(
                Event(
                    EventName.SESSION_CHANGED,
                    session_id=record.session_id,
                    data={
                        **_runtime_data(record, self.clock()),
                        "transcript_path": record.transcript_path,
                    },
                )
            )
            position.binding_signature = binding_signature
            position.transcript_cursor = TranscriptCursor()
            self.state_store.save()

        result = await self.transcript_source.poll(
            record, position.transcript_cursor
        )
        if result.records:
            await self.publisher(
                Event(
                    EventName.SESSION_OUTPUT,
                    session_id=record.session_id,
                    data=_transcript_activity(
                        result.records, result.reset, self.clock()
                    ),
                )
            )
        if result.cursor != position.transcript_cursor:
            position.transcript_cursor = result.cursor
            self.state_store.save()


def _runtime_data(record: LocalSessionRecord, occurred_at: float) -> dict[str, object]:
    return {
        "name": record.name,
        "workdir": record.workdir,
        "window_id": record.window_id,
        "backend": record.backend,
        "provider_session_id": record.provider_session_id,
        "state": record.state.value,
        "last_event_at": occurred_at,
    }


def _runtime_signature(record: LocalSessionRecord) -> str:
    return json.dumps(
        _runtime_data(record, 0.0), separators=(",", ":"), sort_keys=True
    )


def _binding_signature(record: LocalSessionRecord) -> str:
    return json.dumps(
        [record.backend, record.provider_session_id, record.transcript_path],
        separators=(",", ":"),
    )


def _transcript_activity(
    records: tuple[RawTranscriptRecord, ...], reset: bool, occurred_at: float
) -> dict[str, object]:
    event_types = sorted(
        {
            str(record.payload.get("type", ""))[:64]
            for record in records
            if str(record.payload.get("type", ""))
        }
    )[:32]
    first = records[0]
    last = records[-1]
    return {
        "last_event_at": occurred_at,
        "record_count": len(records),
        "start_offset": first.start_offset,
        "end_offset": last.end_offset,
        "event_types": event_types,
        "transcript_reset": reset,
    }
