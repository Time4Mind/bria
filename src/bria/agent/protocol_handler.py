from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import dataclass

from ..protocol.envelope import Envelope, MessageKind
from ..protocol.messages import Command
from .service import AgentService, UnsupportedCommandError


@dataclass(frozen=True, slots=True)
class AgentError:
    code: str
    message: str
    retryable: bool = False

    def to_payload(self) -> dict[str, object]:
        return {
            "code": self.code,
            "message": self.message,
            "retryable": self.retryable,
        }


class AgentProtocolHandler:
    """Translate wire envelopes into safe calls on one AgentService."""

    def __init__(
        self,
        service: AgentService,
        *,
        on_internal_error: Callable[[Exception], Awaitable[None]] | None = None,
    ) -> None:
        self.service = service
        self.on_internal_error = on_internal_error

    async def handle(self, envelope: Envelope) -> Envelope:
        error = self._validate(envelope)
        if error is not None:
            return self._error(envelope, error)
        if envelope.kind is MessageKind.HEARTBEAT:
            return self._reply(envelope, MessageKind.ACK, {"alive": True})
        try:
            command = Command.from_payload(envelope.payload)
            result = await self.service.execute(command)
        except UnsupportedCommandError as exc:
            return self._error(
                envelope, AgentError("unsupported_command", str(exc))
            )
        except LookupError as exc:
            return self._error(envelope, AgentError("not_found", str(exc)))
        except (ValueError, TypeError) as exc:
            return self._error(
                envelope, AgentError("invalid_request", str(exc))
            )
        except Exception as exc:  # transport boundary: never leak internals
            if self.on_internal_error is not None:
                await self.on_internal_error(exc)
            return self._error(
                envelope,
                AgentError(
                    "internal_error",
                    "agent command failed",
                    retryable=True,
                ),
            )
        return self._reply(envelope, MessageKind.RESULT, result)

    def _validate(self, envelope: Envelope) -> AgentError | None:
        if envelope.host_id != self.service.runtime.host_id:
            return AgentError("wrong_host", "command targets a different host")
        if envelope.kind not in {MessageKind.COMMAND, MessageKind.HEARTBEAT}:
            return AgentError(
                "invalid_kind", f"agent cannot handle {envelope.kind.value}"
            )
        if envelope.kind is MessageKind.COMMAND and not envelope.request_id:
            return AgentError("missing_request_id", "request_id is required")
        return None

    @staticmethod
    def _reply(
        request: Envelope,
        kind: MessageKind,
        payload: dict[str, object],
    ) -> Envelope:
        return Envelope(
            kind=kind,
            host_id=request.host_id,
            request_id=request.request_id,
            sequence=request.sequence,
            payload=payload,
        )

    def _error(self, request: Envelope, error: AgentError) -> Envelope:
        return self._reply(request, MessageKind.ERROR, error.to_payload())
