from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path
from typing import Any

from ..protocol.envelope import Envelope, MessageKind, ProtocolError
from ..protocol.messages import Event

_SCHEMA_VERSION = 1


class EventSpoolError(RuntimeError):
    pass


class EventSpool:
    """Durable cumulative-ACK queue for one host's outbound events."""

    def __init__(self, path: Path, *, host_id: str) -> None:
        if not host_id.strip():
            raise ValueError("host_id is required")
        self.path = path
        self.host_id = host_id
        self._acked_sequence = 0
        self._next_sequence = 1
        self._events: list[Envelope] = []
        self._load()

    @property
    def acked_sequence(self) -> int:
        return self._acked_sequence

    @property
    def next_sequence(self) -> int:
        return self._next_sequence

    def append(self, event: Event) -> Envelope:
        envelope = Envelope(
            kind=MessageKind.EVENT,
            host_id=self.host_id,
            sequence=self._next_sequence,
            payload=event.to_payload(),
        )
        events = [*self._events, envelope]
        next_sequence = self._next_sequence + 1
        self._persist(
            acked_sequence=self._acked_sequence,
            next_sequence=next_sequence,
            events=events,
        )
        self._events = events
        self._next_sequence = next_sequence
        return envelope

    def pending(self) -> tuple[Envelope, ...]:
        return tuple(self._events)

    def acknowledge(self, sequence: int) -> bool:
        """Apply an idempotent cumulative ACK; return whether state advanced."""
        if sequence <= 0:
            raise ValueError("ACK sequence must be positive")
        if sequence >= self._next_sequence:
            raise ValueError("ACK sequence was never assigned")
        if sequence <= self._acked_sequence:
            return False
        events = [event for event in self._events if event.sequence > sequence]
        self._persist(
            acked_sequence=sequence,
            next_sequence=self._next_sequence,
            events=events,
        )
        self._acked_sequence = sequence
        self._events = events
        return True

    def _load(self) -> None:
        if not self.path.exists():
            return
        try:
            raw = json.loads(self.path.read_text(encoding="utf-8"))
            if not isinstance(raw, dict):
                raise EventSpoolError("event spool must contain an object")
            self._load_state(raw)
        except (KeyError, TypeError, ValueError, OSError, ProtocolError) as exc:
            raise EventSpoolError(f"cannot load event spool: {self.path}") from exc

    def _load_state(self, raw: dict[str, Any]) -> None:
        if int(raw.get("schema_version", 0)) != _SCHEMA_VERSION:
            raise EventSpoolError("unsupported event spool schema")
        if str(raw.get("host_id", "")) != self.host_id:
            raise EventSpoolError("event spool belongs to a different host")
        acked = int(raw.get("acked_sequence", 0))
        next_sequence = int(raw["next_sequence"])
        records = raw.get("events", [])
        if not isinstance(records, list):
            raise EventSpoolError("event spool events must be an array")
        events = [_decode_event(record) for record in records]
        _validate_state(acked, next_sequence, events, self.host_id)
        self._acked_sequence = acked
        self._next_sequence = next_sequence
        self._events = events

    def _persist(
        self,
        *,
        acked_sequence: int,
        next_sequence: int,
        events: list[Envelope],
    ) -> None:
        payload = {
            "schema_version": _SCHEMA_VERSION,
            "host_id": self.host_id,
            "acked_sequence": acked_sequence,
            "next_sequence": next_sequence,
            "events": [json.loads(event.to_json()) for event in events],
        }
        try:
            encoded = json.dumps(payload, separators=(",", ":"), sort_keys=True)
            self.path.parent.mkdir(parents=True, exist_ok=True)
            fd, temp_name = tempfile.mkstemp(
                prefix=f".{self.path.name}.", dir=self.path.parent, text=True
            )
            temp_path = Path(temp_name)
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as handle:
                    handle.write(encoded + "\n")
                    handle.flush()
                    os.fsync(handle.fileno())
                os.chmod(temp_path, 0o600)
                os.replace(temp_path, self.path)
            finally:
                temp_path.unlink(missing_ok=True)
        except (OSError, TypeError, ValueError) as exc:
            raise EventSpoolError(f"cannot persist event spool: {self.path}") from exc


def _decode_event(record: object) -> Envelope:
    if not isinstance(record, dict):
        raise EventSpoolError("event spool record must be an object")
    envelope = Envelope.from_json(json.dumps(record))
    Event.from_payload(envelope.payload)
    return envelope


def _validate_state(
    acked: int,
    next_sequence: int,
    events: list[Envelope],
    host_id: str,
) -> None:
    if acked < 0 or next_sequence <= acked:
        raise EventSpoolError("invalid event spool sequence bounds")
    sequences = [event.sequence for event in events]
    if sequences != list(range(acked + 1, next_sequence)):
        raise EventSpoolError("event spool sequence contains a gap")
    if any(
        event.kind is not MessageKind.EVENT
        or event.host_id != host_id
        or bool(event.request_id)
        for event in events
    ):
        raise EventSpoolError("invalid event envelope in spool")
