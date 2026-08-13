from __future__ import annotations

from bria.agent.protocol_handler import AgentProtocolHandler
from bria.agent.service import AgentService
from bria.protocol.envelope import Envelope, MessageKind
from bria.protocol.messages import Command, CommandName
from bria.runtime.memory import MemoryRuntime


def _handler(host_id: str = "server-a") -> AgentProtocolHandler:
    runtime = MemoryRuntime(host_id)
    service = AgentService(
        runtime,
        host_name="Server A",
        home_dir="/home/agent",
        agent_version="test",
    )
    return AgentProtocolHandler(service)


async def test_protocol_handler_executes_command() -> None:
    handler = _handler()
    command = Command(CommandName.HEALTH)
    request = Envelope.new_request(
        MessageKind.COMMAND, "server-a", command.to_payload()
    )

    response = await handler.handle(request)

    assert response.kind is MessageKind.RESULT
    assert response.request_id == request.request_id
    assert response.payload["status"] == "online"


async def test_protocol_handler_rejects_wrong_host_without_execution() -> None:
    handler = _handler()
    request = Envelope.new_request(MessageKind.COMMAND, "server-b", {})

    response = await handler.handle(request)

    assert response.kind is MessageKind.ERROR
    assert response.payload["code"] == "wrong_host"


async def test_protocol_handler_conceals_internal_errors() -> None:
    handler = _handler()
    command = Command(
        CommandName.UPLOAD_FILE,
        {"session_id": "missing", "name": "x", "content_base64": "%%%"},
    )
    request = Envelope.new_request(
        MessageKind.COMMAND, "server-a", command.to_payload()
    )

    response = await handler.handle(request)

    assert response.kind is MessageKind.ERROR
    assert response.payload["code"] == "invalid_request"
