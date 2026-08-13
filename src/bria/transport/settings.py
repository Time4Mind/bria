from __future__ import annotations

from dataclasses import dataclass

from websockets.typing import Subprotocol


@dataclass(frozen=True, slots=True)
class WebSocketSettings:
    """Finite limits shared by both sides of a WebSocket channel."""

    open_timeout: float = 10.0
    hello_timeout: float = 10.0
    request_timeout: float = 30.0
    close_timeout: float = 5.0
    ping_interval: float = 20.0
    ping_timeout: float = 20.0
    max_message_size: int = 2 * 1024 * 1024
    max_inflight_commands: int = 32

    def __post_init__(self) -> None:
        durations = (
            self.open_timeout,
            self.hello_timeout,
            self.request_timeout,
            self.close_timeout,
            self.ping_interval,
            self.ping_timeout,
        )
        if any(value <= 0 for value in durations):
            raise ValueError("WebSocket timeouts must be positive")
        if self.max_message_size <= 0:
            raise ValueError("max_message_size must be positive")
        if self.max_inflight_commands <= 0:
            raise ValueError("max_inflight_commands must be positive")


SUBPROTOCOL = Subprotocol("bria.v1")
HOST_ID_HEADER = "X-Bria-Host-ID"
