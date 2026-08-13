from __future__ import annotations

from ..protocol.envelope import Envelope, ProtocolError


def decode_envelope(frame: str | bytes) -> Envelope:
    if isinstance(frame, bytes):
        raise ProtocolError("binary WebSocket messages aren't supported")
    return Envelope.from_json(frame)
