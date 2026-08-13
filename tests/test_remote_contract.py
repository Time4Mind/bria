from __future__ import annotations

from bria.agent.service import AgentService
from bria.protocol.envelope import Envelope, MessageKind
from bria.protocol.messages import Command
from bria.runtime.base import CreateSessionRequest
from bria.runtime.memory import MemoryRuntime
from bria.runtime.remote import RemoteRuntime


class LoopbackTransport:
    def __init__(self, host_id: str, agent: AgentService) -> None:
        self._host_id = host_id
        self.agent = agent

    @property
    def host_id(self) -> str:
        return self._host_id

    async def request(self, message: Envelope) -> Envelope:
        result = await self.agent.execute(Command.from_payload(message.payload))
        return Envelope(
            kind=MessageKind.RESULT,
            host_id=self.host_id,
            request_id=message.request_id,
            payload=result,
        )

    async def close(self) -> None:
        pass


async def test_remote_runtime_has_same_contract_as_local_runtime() -> None:
    local = MemoryRuntime("server-a")
    agent = AgentService(
        local,
        host_name="Server A",
        home_dir="/home/bot",
        agent_version="test",
    )
    remote = RemoteRuntime(LoopbackTransport("server-a", agent))
    request = CreateSessionRequest("session-1", "/srv/app", "app")

    created = await remote.create_session(request)
    await remote.send_text(created.session_id, "run tests")
    capture = await remote.capture_pane(created.session_id)

    assert created.session_id == "session-1"
    assert local.sent_text == [("session-1", "run tests")]
    assert capture.text == "server-a:app"
