from __future__ import annotations

from dataclasses import dataclass

from ..domain.enums import SessionViewMode
from ..domain.host import Host
from ..domain.session import Session


@dataclass(frozen=True, slots=True)
class SessionListItem:
    session: Session
    host: Host
    is_selected: bool = False

    @property
    def qualified_name(self) -> str:
        """UI-neutral label that remains unambiguous in a global list."""
        return f"{self.session.name} · {self.host.name}"


@dataclass(frozen=True, slots=True)
class SessionListing:
    mode: SessionViewMode
    selected_host: Host
    items: tuple[SessionListItem, ...]

    @property
    def is_global(self) -> bool:
        return self.mode is SessionViewMode.ALL_HOSTS
