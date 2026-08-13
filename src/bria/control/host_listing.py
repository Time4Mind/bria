from __future__ import annotations

from dataclasses import dataclass

from ..domain.host import Host


@dataclass(frozen=True, slots=True)
class HostListItem:
    host: Host
    live_session_count: int
    is_selected: bool = False


@dataclass(frozen=True, slots=True)
class HostListing:
    items: tuple[HostListItem, ...]
