from __future__ import annotations

import socket
import time
from dataclasses import dataclass, field
from typing import Any

from .enums import Capability, HostKind, HostStatus

LOCAL_HOST_ID = "local"


@dataclass(slots=True)
class Host:
    """A stable execution target; ``name`` may change, ``id`` may not."""

    id: str
    name: str
    kind: HostKind = HostKind.AGENT
    status: HostStatus = HostStatus.OFFLINE
    home_dir: str = ""
    agent_version: str = ""
    runtime_version: str = ""
    capabilities: set[Capability] = field(default_factory=set)
    last_seen_at: float = 0.0
    last_event_sequence: int = 0
    enabled: bool = True

    @classmethod
    def local(cls, name: str | None = None) -> Host:
        return cls(
            id=LOCAL_HOST_ID,
            name=(name or socket.gethostname()).strip() or "local",
            kind=HostKind.LOCAL,
            status=HostStatus.ONLINE,
            capabilities=set(Capability),
            last_seen_at=time.time(),
        )

    @property
    def reachable(self) -> bool:
        return self.enabled and self.status is HostStatus.ONLINE

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "name": self.name,
            "kind": self.kind.value,
            "status": self.status.value,
            "home_dir": self.home_dir,
            "agent_version": self.agent_version,
            "runtime_version": self.runtime_version,
            "capabilities": sorted(item.value for item in self.capabilities),
            "last_seen_at": self.last_seen_at,
            "last_event_sequence": self.last_event_sequence,
            "enabled": self.enabled,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Host:
        return cls(
            id=str(data["id"]),
            name=str(data.get("name") or data["id"]),
            kind=HostKind(str(data.get("kind", HostKind.AGENT.value))),
            status=HostStatus(str(data.get("status", HostStatus.OFFLINE.value))),
            home_dir=str(data.get("home_dir", "")),
            agent_version=str(data.get("agent_version", "")),
            runtime_version=str(data.get("runtime_version", "")),
            capabilities={
                Capability(str(value)) for value in data.get("capabilities", [])
            },
            last_seen_at=float(data.get("last_seen_at", 0.0)),
            last_event_sequence=int(data.get("last_event_sequence", 0)),
            enabled=bool(data.get("enabled", True)),
        )
