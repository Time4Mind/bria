from __future__ import annotations

import re

from ...domain.host import Host
from ...domain.session import Session
from ..callback_data import callback, entity_token
from ..views import Button, Screen

_ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


def render_card(session: Session, host: Host, pane: str) -> Screen:
    terminal = _clean_terminal(pane)
    body = (
        f"{session.name} · {host.name}\n"
        f"{session.backend} · {session.state.value}\n"
        f"{session.workdir}\n\n"
        f"{terminal or '(terminal is empty)'}"
    )
    return Screen(
        body[:3900],
        (
            (
                Button(
                    "Refresh",
                    callback("card", entity_token("session", session.id)),
                ),
                Button("Sessions", callback("sessions")),
            ),
            (
                Button(
                    "Archive",
                    callback("archive", entity_token("session", session.id)),
                ),
                Button("Menu", callback("home")),
            ),
        ),
        screen_name="card",
        session_id=session.id,
    )


def _clean_terminal(pane: str) -> str:
    text = _ANSI.sub("", pane).replace("\x00", "").strip()
    return text[-3300:]


def render_archive_confirmation(session: Session, host: Host) -> Screen:
    token = entity_token("session", session.id)
    return Screen(
        f"Archive {session.name} on {host.name}?\n\n"
        "The provider session ID is kept and restore will place it at the end "
        "of the live list using the restore timestamp.",
        (
            (
                Button("Archive", callback("archive_do", token)),
                Button("Cancel", callback("card", token)),
            ),
        ),
        screen_name="archive_confirmation",
        session_id=session.id,
    )
