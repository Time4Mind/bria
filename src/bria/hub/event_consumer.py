from __future__ import annotations

import copy
import math
import time
from collections.abc import Callable
from typing import Any

from ..domain.enums import LIVE_SESSION_STATES, HostStatus, SessionState
from ..domain.project_state import ProjectState
from ..domain.session import Session
from ..persistence.base import StateStore
from ..protocol.envelope import Envelope, MessageKind, ProtocolError
from ..protocol.messages import Event, EventName
from .event_stream import AppliedHostEvent

_ACTIVITY_EVENTS = frozenset({EventName.SESSION_OUTPUT, EventName.INTERACTIVE_PROMPT})
_ALLOWED_TRANSITIONS: dict[SessionState, frozenset[SessionState]] = {
    SessionState.ACTIVE: frozenset(SessionState),
    SessionState.IDLE: frozenset(SessionState),
    SessionState.ARCHIVED: frozenset({SessionState.ACTIVE, SessionState.IDLE}),
    SessionState.COMPLETED: frozenset({SessionState.ACTIVE, SessionState.IDLE}),
    SessionState.LOST: frozenset(
        {SessionState.ACTIVE, SessionState.IDLE, SessionState.ARCHIVED}
    ),
}


class HubEventConsumer:
    """Apply one authenticated host's sequenced events after durable staging."""

    def __init__(
        self,
        state: ProjectState,
        store: StateStore,
        *,
        host_id: str,
        clock: Callable[[], float] = time.time,
        after_commit: Callable[[AppliedHostEvent], None] | None = None,
    ) -> None:
        self.state = state
        self.store = store
        self.host_id = host_id
        self.clock = clock
        self.after_commit = after_commit

    def consume(self, envelope: Envelope) -> Envelope:
        host = self._validate_envelope(envelope)
        if envelope.sequence <= host.last_event_sequence:
            return self._ack(envelope, host.last_event_sequence, duplicate=True)
        expected = host.last_event_sequence + 1
        if envelope.sequence != expected:
            raise ProtocolError(
                f"event sequence gap for {self.host_id}: "
                f"expected {expected}, got {envelope.sequence}"
            )
        try:
            event = Event.from_payload(envelope.payload)
        except (KeyError, TypeError, ValueError) as exc:
            raise ProtocolError("invalid event payload") from exc

        staged = copy.deepcopy(self.state)
        try:
            self._apply(staged, event, envelope)
        except ProtocolError:
            raise
        except (TypeError, ValueError) as exc:
            raise ProtocolError("invalid event metadata") from exc
        staged_host = staged.hosts[self.host_id]
        staged_host.last_event_sequence = envelope.sequence
        staged_host.last_seen_at = self.clock()
        self.store.save(staged)
        self._commit(staged)
        if self.after_commit is not None:
            self.after_commit(AppliedHostEvent(envelope, event))
        return self._ack(envelope, envelope.sequence, duplicate=False)

    def _validate_envelope(self, envelope: Envelope):
        if envelope.kind is not MessageKind.EVENT:
            raise ProtocolError(f"expected event, got {envelope.kind.value}")
        if envelope.host_id != self.host_id:
            raise ProtocolError("event came from a different host")
        if envelope.sequence <= 0:
            raise ProtocolError("event sequence must be positive")
        try:
            return self.state.hosts[self.host_id]
        except KeyError as exc:
            raise ProtocolError(f"unknown authenticated host: {self.host_id}") from exc

    def _apply(self, state: ProjectState, event: Event, envelope: Envelope) -> None:
        if event.name is EventName.HOST_STATUS:
            self._apply_host_status(state, event)
            return
        if event.name is EventName.SESSION_ANNOUNCED:
            session = self._announced_session(state, event, envelope)
            self._apply_session_metadata(session, event.data, envelope)
            return
        if event.name is EventName.SESSION_CHANGED:
            session = self._known_session(state, event)
            self._apply_session_metadata(session, event.data, envelope)
            return
        if event.name in _ACTIVITY_EVENTS:
            session = self._known_session(state, event)
            session.last_event_at = self._event_time(event.data, envelope)

    def _apply_host_status(self, state: ProjectState, event: Event) -> None:
        host = state.hosts[self.host_id]
        if "status" in event.data:
            host.status = HostStatus(_required_string(event.data, "status", 32))
        name = _optional_string(event.data, "name", 256)
        runtime_version = _optional_string(event.data, "runtime_version", 128)
        if name is not None:
            host.name = name
        if runtime_version is not None:
            host.runtime_version = runtime_version

    def _announced_session(
        self, state: ProjectState, event: Event, envelope: Envelope
    ) -> Session:
        session_id = self._session_id(event)
        existing = state.sessions.get(session_id)
        if existing is not None:
            if existing.host_id != self.host_id:
                raise ProtocolError(f"session ID belongs to another host: {session_id}")
            return existing
        occurred_at = self._event_time(event.data, envelope)
        created_at = _optional_timestamp(event.data, "created_at") or occurred_at
        session = Session(
            id=session_id,
            name=_required_string(event.data, "name", 256),
            host_id=self.host_id,
            workdir=_required_string(event.data, "workdir", 4096),
            backend=_optional_string(event.data, "backend", 32) or "claude",
            created_at=created_at,
            live_since_at=created_at,
            last_event_at=occurred_at,
        )
        state.sessions[session.id] = session
        return session

    def _known_session(self, state: ProjectState, event: Event) -> Session:
        session_id = self._session_id(event)
        session = state.sessions.get(session_id)
        if session is None:
            raise ProtocolError(f"event references unknown session: {session_id}")
        if session.host_id != self.host_id:
            raise ProtocolError(f"session belongs to another host: {session_id}")
        return session

    def _apply_session_metadata(
        self, session: Session, data: dict[str, Any], envelope: Envelope
    ) -> None:
        occurred_at = self._event_time(data, envelope)
        for field_name, max_length in (
            ("name", 256),
            ("workdir", 4096),
            ("window_id", 256),
            ("provider_session_id", 256),
            ("backend", 32),
        ):
            value = _optional_string(data, field_name, max_length)
            if value is not None:
                setattr(session, field_name, value)
        if "state" in data:
            new_state = SessionState(_required_string(data, "state", 32))
            self._transition(session, new_state, occurred_at)
        session.last_event_at = occurred_at

    @staticmethod
    def _transition(
        session: Session, new_state: SessionState, occurred_at: float
    ) -> None:
        if new_state is session.state:
            return
        if new_state not in _ALLOWED_TRANSITIONS[session.state]:
            raise ProtocolError(
                f"invalid session transition: {session.state} -> {new_state}"
            )
        was_live = session.state in LIVE_SESSION_STATES
        will_be_live = new_state in LIVE_SESSION_STATES
        session.state = new_state
        if was_live and not will_be_live:
            session.window_id = ""
            if new_state in {SessionState.ARCHIVED, SessionState.COMPLETED}:
                session.archived_at = occurred_at
        elif not was_live and will_be_live:
            session.restored_at = occurred_at
            session.live_since_at = occurred_at

    @staticmethod
    def _session_id(event: Event) -> str:
        session_id = event.session_id.strip()
        if not session_id or len(session_id) > 128:
            raise ProtocolError("event session_id is invalid")
        return session_id

    def _event_time(self, data: dict[str, Any], envelope: Envelope) -> float:
        timestamp = _optional_timestamp(data, "last_event_at")
        if timestamp is not None:
            return timestamp
        if envelope.sent_at > 0 and math.isfinite(envelope.sent_at):
            return envelope.sent_at
        return self.clock()

    def _commit(self, staged: ProjectState) -> None:
        self.state.schema_version = staged.schema_version
        self.state.hosts = staged.hosts
        self.state.sessions = staged.sessions
        self.state.navigation = staged.navigation
        self.state.preferences = staged.preferences

    def _ack(self, request: Envelope, sequence: int, *, duplicate: bool) -> Envelope:
        return Envelope(
            kind=MessageKind.ACK,
            host_id=self.host_id,
            request_id=request.request_id,
            sequence=sequence,
            payload={"last_applied_sequence": sequence, "duplicate": duplicate},
        )


def _optional_string(data: dict[str, Any], key: str, max_length: int) -> str | None:
    if key not in data:
        return None
    value = data[key]
    if not isinstance(value, str) or len(value) > max_length:
        raise ProtocolError(f"event field is not a valid string: {key}")
    return value


def _required_string(data: dict[str, Any], key: str, max_length: int) -> str:
    value = _optional_string(data, key, max_length)
    if value is None or not value.strip():
        raise ProtocolError(f"event field is required: {key}")
    return value


def _optional_timestamp(data: dict[str, Any], key: str) -> float | None:
    if key not in data:
        return None
    value = data[key]
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ProtocolError(f"event field is not a valid timestamp: {key}")
    timestamp = float(value)
    if timestamp < 0 or not math.isfinite(timestamp):
        raise ProtocolError(f"event field is not a valid timestamp: {key}")
    return timestamp
