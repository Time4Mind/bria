from __future__ import annotations

from ...control.host_listing import HostListing
from ...domain.enums import HostStatus
from ..callback_data import callback, entity_token
from ..views import Button, Screen
from .menu_rows import menu_row, settings_row

_STATUS = {
    HostStatus.ONLINE: "🟢",
    HostStatus.RECONNECTING: "🟡",
    HostStatus.OFFLINE: "🔴",
    HostStatus.DISABLED: "⚫",
}


def render_home() -> Screen:
    return Screen(
        "Bria\n\nChoose a server or open the global session pool.",
        (menu_row(), settings_row()),
        screen_name="home",
    )


def render_hosts(listing: HostListing) -> Screen:
    rows = tuple(
        (
            Button(
                (
                    f"{_STATUS[item.host.status]} "
                    f"{'✓ ' if item.is_selected else ''}{item.host.name} "
                    f"({item.live_session_count})"
                ),
                callback("host", entity_token("host", item.host.id)),
            ),
        )
        for item in listing.items
    )
    if not rows:
        rows = ((Button("No enabled servers", callback("home")),),)
    return Screen(
        "Servers\n\nSelect a server to see its live sessions.",
        (*rows, (Button("Back", callback("home")),)),
        screen_name="hosts",
    )
