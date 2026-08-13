from __future__ import annotations

import base64

from ..protocol.messages import Command, CommandName, Hello
from ..protocol.version import PROTOCOL_VERSION
from ..runtime.base import CreateSessionRequest, HostRuntime
from .serialization import (
    capture_payload,
    directory_payload,
    health_payload,
    session_payload,
)


class UnsupportedCommandError(ValueError):
    pass


class AgentService:
    """Executes a small allowlist of commands against one host runtime."""

    def __init__(
        self,
        runtime: HostRuntime,
        *,
        host_name: str,
        home_dir: str,
        agent_version: str,
    ) -> None:
        self.runtime = runtime
        self.host_name = host_name
        self.home_dir = home_dir
        self.agent_version = agent_version
        self._completed: dict[str, dict[str, object]] = {}

    async def hello(self, *, last_acked_sequence: int = 0) -> Hello:
        health = await self.runtime.health()
        return Hello(
            agent_version=self.agent_version,
            protocol_version=PROTOCOL_VERSION,
            host_name=self.host_name,
            home_dir=self.home_dir,
            capabilities=health.capabilities,
            last_acked_sequence=last_acked_sequence,
        )

    async def execute(self, command: Command) -> dict[str, object]:
        if command.idempotency_key in self._completed:
            return self._completed[command.idempotency_key]
        result = await self._dispatch(command)
        if command.idempotency_key:
            self._completed[command.idempotency_key] = result
        return result

    async def _dispatch(self, command: Command) -> dict[str, object]:
        args = command.arguments
        match command.name:
            case CommandName.HEALTH:
                return health_payload(await self.runtime.health())
            case CommandName.SNAPSHOT:
                sessions = await self.runtime.snapshot()
                return {"sessions": [session_payload(item) for item in sessions]}
            case CommandName.LIST_DIRECTORIES:
                entries = await self.runtime.list_directories(str(args.get("path", "")))
                return {"entries": [directory_payload(item) for item in entries]}
            case CommandName.CREATE_SESSION:
                request = CreateSessionRequest(
                    session_id=_required(args, "session_id"),
                    workdir=_required(args, "workdir"),
                    name=_required(args, "name"),
                    backend=str(args.get("backend", "claude")),
                    resume_provider_session_id=str(
                        args.get("resume_provider_session_id", "")
                    ),
                )
                return session_payload(await self.runtime.create_session(request))
            case CommandName.SEND_TEXT:
                await self.runtime.send_text(
                    _required(args, "session_id"), _required(args, "text")
                )
                return {"accepted": True}
            case CommandName.SEND_KEY:
                await self.runtime.send_key(
                    _required(args, "session_id"), _required(args, "key")
                )
                return {"accepted": True}
            case CommandName.CAPTURE_PANE:
                result = await self.runtime.capture_pane(_required(args, "session_id"))
                return capture_payload(result)
            case CommandName.ARCHIVE_SESSION:
                await self.runtime.archive_session(_required(args, "session_id"))
                return {"archived": True}
            case CommandName.RESTORE_SESSION:
                result = await self.runtime.restore_session(
                    _required(args, "session_id")
                )
                return session_payload(result)
            case CommandName.UPLOAD_FILE:
                content = base64.b64decode(
                    _required(args, "content_base64"), validate=True
                )
                path = await self.runtime.upload_file(
                    _required(args, "session_id"), _required(args, "name"), content
                )
                return {"path": path}
            case _:
                raise UnsupportedCommandError(
                    f"command is not implemented by this agent: {command.name}"
                )


def _required(arguments: dict[str, object], name: str) -> str:
    value = str(arguments.get(name, ""))
    if not value:
        raise ValueError(f"command argument is required: {name}")
    return value
