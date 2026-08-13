from __future__ import annotations

from dataclasses import dataclass

from ..domain.enums import SessionViewMode
from ..domain.host import Host
from ..domain.session import Session


@dataclass(frozen=True, slots=True)
class ArchivedSessionListItem:
    session: Session
    host: Host

    @property
    def qualified_name(self) -> str:
        """UI-neutral label that identifies the source host."""
        return f"{self.session.name} · {self.host.name}"


@dataclass(frozen=True, slots=True)
class ArchiveListing:
    mode: SessionViewMode
    selected_host: Host
    items: tuple[ArchivedSessionListItem, ...]

    @property
    def is_global(self) -> bool:
        return self.mode is SessionViewMode.ALL_HOSTS
