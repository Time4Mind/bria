from __future__ import annotations

import pytest

from bria.protocol.envelope import (
    Envelope,
    MessageKind,
    ProtocolError,
)
from bria.protocol.messages import Command, CommandName


def test_envelope_round_trip() -> None:
    command = Command(
        CommandName.SEND_TEXT,
        {"session_id": "abc", "text": "hello"},
        idempotency_key="once",
    )
    envelope = Envelope.new_request(
        MessageKind.COMMAND, "server-a", command.to_payload()
    )

    decoded = Envelope.from_json(envelope.to_json())

    assert decoded == envelope
    assert Command.from_payload(decoded.payload) == command


def test_rejects_unknown_protocol_version() -> None:
    raw = '{"version":99,"kind":"heartbeat","host_id":"server-a"}'

    with pytest.raises(ProtocolError, match="unsupported protocol version"):
        Envelope.from_json(raw)
