from __future__ import annotations

from ...control.archive_listing import ArchiveListing
from ...control.session_listing import SessionListing
from ...domain.enums import SessionViewMode
from ..callback_data import callback, entity_token
from ..views import Button, Screen


def render_sessions(listing: SessionListing) -> Screen:
    title = (
        "All live sessions"
        if listing.is_global
        else f"Sessions · {listing.selected_host.name}"
    )
    rows = tuple(
        (
            Button(
                f"{'✓ ' if item.is_selected else ''}"
                f"{item.qualified_name if listing.is_global else item.session.name}",
                callback("session", entity_token("session", item.session.id)),
            ),
        )
        for item in listing.items
    )
    empty = "No live sessions." if not rows else ""
    back = "hosts" if listing.mode is SessionViewMode.HOST_FIRST else "home"
    return Screen(
        f"{title}\n\n{empty}".rstrip(),
        (*rows, (Button("Back", callback(back)),)),
        screen_name="sessions",
    )


def render_archive(listing: ArchiveListing) -> Screen:
    title = (
        "Archive · all servers"
        if listing.is_global
        else f"Archive · {listing.selected_host.name}"
    )
    rows = tuple(
        (
            Button(
                "Restore "
                f"{item.qualified_name if listing.is_global else item.session.name}",
                callback("restore", entity_token("session", item.session.id)),
            ),
        )
        for item in listing.items
    )
    empty = "No archived sessions." if not rows else ""
    return Screen(
        f"{title}\n\n{empty}".rstrip(),
        (*rows, (Button("Back", callback("home")),)),
        screen_name="archive",
    )
