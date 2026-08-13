from __future__ import annotations

import pytest

from bria.domain.enums import SessionState
from bria.domain.host import Host
from bria.domain.project_state import ProjectState
from bria.hub.event_consumer import HubEventConsumer
from bria.persistence.json_store import JsonStateStore
from bria.protocol.envelope import Envelope, MessageKind, ProtocolError
from bria.protocol.messages import Event, EventName


class RecordingStore:
    def __init__(self) -> None:
        self.saved: list[dict[str, object]] = []

    def load(self) -> ProjectState:
        raise NotImplementedError

    def save(self, state: ProjectState) -> None:
        self.saved.append(state.to_dict())


class FailingStore(RecordingStore):
    def save(self, state: ProjectState) -> None:
        raise OSError("disk unavailable")


def event_envelope(
    host_id: str,
    sequence: int,
    event: Event,
    *,
    request_id: str = "event-request",
    sent_at: float = 10.0,
) -> Envelope:
    return Envelope(
        kind=MessageKind.EVENT,
        host_id=host_id,
        sequence=sequence,
        request_id=request_id,
        sent_at=sent_at,
        payload=event.to_payload(),
    )


def make_state() -> ProjectState:
    state = ProjectState.empty()
    state.hosts["server-a"] = Host(id="server-a", name="Server A")
    state.hosts["server-b"] = Host(id="server-b", name="Server B")
    return state


def announce(sequence: int = 1) -> Envelope:
    return event_envelope(
        "server-a",
        sequence,
        Event(
            EventName.SESSION_ANNOUNCED,
            session_id="session-a",
            data={
                "name": "Build",
                "workdir": "/srv/build",
                "window_id": "@1",
                "provider_session_id": "provider-1",
                "backend": "claude",
                "state": "active",
                "created_at": 5.0,
                "last_event_at": 10.0,
                "transcript": "must not be persisted",
            },
        ),
    )


def test_announced_session_is_applied_before_ack_and_metadata_is_safe() -> None:
    state = make_state()
    store = RecordingStore()
    consumer = HubEventConsumer(state, store, host_id="server-a", clock=lambda: 20.0)

    ack = consumer.consume(announce())

    session = state.sessions["session-a"]
    assert ack.kind is MessageKind.ACK
    assert ack.sequence == 1
    assert ack.payload == {"last_applied_sequence": 1, "duplicate": False}
    assert state.hosts["server-a"].last_event_sequence == 1
    assert store.saved[-1]["hosts"]["server-a"]["last_event_sequence"] == 1  # type: ignore[index]
    assert session.created_at == 5.0
    assert session.last_event_at == 10.0
    assert session.provider_session_id == "provider-1"
    assert "transcript" not in session.to_dict()


def test_duplicate_is_acknowledged_without_reapply_or_save() -> None:
    state = make_state()
    store = RecordingStore()
    consumer = HubEventConsumer(state, store, host_id="server-a")
    consumer.consume(announce())
    duplicate = announce()
    duplicate.payload["data"]["name"] = "Wrong duplicate name"  # type: ignore[index]

    ack = consumer.consume(duplicate)

    assert ack.payload == {"last_applied_sequence": 1, "duplicate": True}
    assert state.sessions["session-a"].name == "Build"
    assert len(store.saved) == 1


def test_sequence_gap_is_rejected_without_state_change() -> None:
    state = make_state()
    store = RecordingStore()
    consumer = HubEventConsumer(state, store, host_id="server-a")
    before = state.to_dict()

    with pytest.raises(ProtocolError, match="expected 1, got 2"):
        consumer.consume(announce(sequence=2))

    assert state.to_dict() == before
    assert store.saved == []


def test_wrong_authenticated_host_is_rejected() -> None:
    state = make_state()
    store = RecordingStore()
    consumer = HubEventConsumer(state, store, host_id="server-a")
    message = event_envelope(
        "server-b",
        1,
        Event(EventName.HOST_STATUS, data={"status": "online"}),
    )

    with pytest.raises(ProtocolError, match="different host"):
        consumer.consume(message)

    assert state.hosts["server-a"].last_event_sequence == 0
    assert store.saved == []


def test_unknown_session_requires_an_explicit_announcement() -> None:
    state = make_state()
    store = RecordingStore()
    consumer = HubEventConsumer(state, store, host_id="server-a")
    changed = event_envelope(
        "server-a",
        1,
        Event(
            EventName.SESSION_CHANGED,
            session_id="unknown",
            data={"state": "idle"},
        ),
    )

    with pytest.raises(ProtocolError, match="unknown session"):
        consumer.consume(changed)

    assert "unknown" not in state.sessions
    assert state.hosts["server-a"].last_event_sequence == 0


def test_session_changes_update_metadata_and_transition_timestamps() -> None:
    state = make_state()
    store = RecordingStore()
    consumer = HubEventConsumer(state, store, host_id="server-a")
    consumer.consume(announce())
    archived = event_envelope(
        "server-a",
        2,
        Event(
            EventName.SESSION_CHANGED,
            session_id="session-a",
            data={"state": "archived", "last_event_at": 30.0},
        ),
    )
    restored = event_envelope(
        "server-a",
        3,
        Event(
            EventName.SESSION_CHANGED,
            session_id="session-a",
            data={
                "state": "active",
                "window_id": "@9",
                "provider_session_id": "provider-2",
                "last_event_at": 40.0,
            },
        ),
    )

    consumer.consume(archived)
    consumer.consume(restored)

    session = state.sessions["session-a"]
    assert session.state is SessionState.ACTIVE
    assert session.created_at == 5.0
    assert session.archived_at == 30.0
    assert session.restored_at == 40.0
    assert session.live_since_at == 40.0
    assert session.window_id == "@9"
    assert session.provider_session_id == "provider-2"


def test_ack_progress_and_session_survive_json_reload(tmp_path) -> None:
    path = tmp_path / "state.json"
    store = JsonStateStore(path)
    state = make_state()
    store.save(state)
    consumer = HubEventConsumer(state, store, host_id="server-a")

    ack = consumer.consume(announce())
    loaded = store.load()

    assert ack.sequence == 1
    assert loaded.hosts["server-a"].last_event_sequence == 1
    assert loaded.sessions["session-a"].name == "Build"


def test_store_failure_does_not_advance_in_memory_sequence_or_session() -> None:
    state = make_state()
    consumer = HubEventConsumer(state, FailingStore(), host_id="server-a")
    before = state.to_dict()

    with pytest.raises(OSError, match="disk unavailable"):
        consumer.consume(announce())

    assert state.to_dict() == before
