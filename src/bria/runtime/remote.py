from __future__ import annotations

import base64
from typing import Any

from ..domain.enums import Capability, HostStatus, SessionState
from ..protocol.envelope import Envelope, MessageKind, ProtocolError
from ..protocol.messages import Command, CommandName
from ..transport.base import HubTransport
from .base import (
    CaptureResult,
    CreateSessionRequest,
    DirectoryEntry,
    HostHealth,
    RuntimeSession,
)


class RemoteRuntime:
    """HostRuntime adapter that turns method calls into protocol commands."""

    def __init__(self, transport: HubTransport) -> None:
        self.transport = transport

    @property
    def host_id(self) -> str:
        return self.transport.host_id

    async def health(self) -> HostHealth:
        data = await self._request(CommandName.HEALTH)
        return HostHealth(
            status=HostStatus(str(data["status"])),
            detail=str(data.get("detail", "")),
            version=str(data.get("version", "")),
            capabilities=frozenset(
                Capability(str(item)) for item in data.get("capabilities", [])
            ),
        )

    async def snapshot(self) -> list[RuntimeSession]:
        data = await self._request(CommandName.SNAPSHOT)
        sessions = data.get("sessions", [])
        if not isinstance(sessions, list):
            raise ProtocolError("snapshot sessions must be an array")
        return [_runtime_session(_mapping(item)) for item in sessions]

    async def list_directories(self, path: str) -> list[DirectoryEntry]:
        data = await self._request(CommandName.LIST_DIRECTORIES, path=path)
        entries = data.get("entries", [])
        if not isinstance(entries, list):
            raise ProtocolError("directory entries must be an array")
        return [
            DirectoryEntry(
                name=str(item["name"]),
                path=str(item["path"]),
                modified_at=float(item.get("modified_at", 0.0)),
            )
            for raw in entries
            for item in [_mapping(raw)]
        ]

    async def create_session(self, request: CreateSessionRequest) -> RuntimeSession:
        data = await self._request(
            CommandName.CREATE_SESSION,
            session_id=request.session_id,
            workdir=request.workdir,
            name=request.name,
            backend=request.backend,
            resume_provider_session_id=request.resume_provider_session_id,
        )
        return _runtime_session(data)

    async def send_text(self, session_id: str, text: str) -> None:
        await self._request(CommandName.SEND_TEXT, session_id=session_id, text=text)

    async def send_key(self, session_id: str, key: str) -> None:
        await self._request(CommandName.SEND_KEY, session_id=session_id, key=key)

    async def capture_pane(self, session_id: str) -> CaptureResult:
        data = await self._request(CommandName.CAPTURE_PANE, session_id=session_id)
        metadata = data.get("metadata", {})
        return CaptureResult(
            text=str(data.get("text", "")),
            ansi=bool(data.get("ansi", True)),
            metadata={
                str(key): str(value)
                for key, value in _mapping(metadata).items()
            },
        )

    async def archive_session(self, session_id: str) -> None:
        await self._request(CommandName.ARCHIVE_SESSION, session_id=session_id)

    async def restore_session(self, session_id: str) -> RuntimeSession:
        data = await self._request(CommandName.RESTORE_SESSION, session_id=session_id)
        return _runtime_session(data)

    async def upload_file(self, session_id: str, name: str, content: bytes) -> str:
        data = await self._request(
            CommandName.UPLOAD_FILE,
            session_id=session_id,
            name=name,
            content_base64=base64.b64encode(content).decode("ascii"),
        )
        return str(data["path"])

    async def _request(
        self, command_name: CommandName, **arguments: object
    ) -> dict[str, Any]:
        command = Command(name=command_name, arguments=arguments)
        request = Envelope.new_request(
            MessageKind.COMMAND, self.host_id, command.to_payload()
        )
        response = await self.transport.request(request)
        if response.request_id != request.request_id:
            raise ProtocolError("response request_id does not match request")
        if response.kind is MessageKind.ERROR:
            raise ProtocolError(str(response.payload.get("message", "remote error")))
        if response.kind is not MessageKind.RESULT:
            raise ProtocolError(f"expected result, got {response.kind}")
        return response.payload


def _mapping(value: object) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ProtocolError("expected an object")
    return value


def _runtime_session(data: dict[str, Any]) -> RuntimeSession:
    return RuntimeSession(
        session_id=str(data["session_id"]),
        window_id=str(data.get("window_id", "")),
        workdir=str(data.get("workdir", "")),
        name=str(data.get("name", "")),
        backend=str(data.get("backend", "claude")),
        provider_session_id=str(data.get("provider_session_id", "")),
        state=SessionState(str(data.get("state", SessionState.ACTIVE.value))),
    )
