from __future__ import annotations

from ..callback_data import callback
from ..views import Button


def menu_row() -> tuple[Button, ...]:
    return (
        Button("Servers / sessions", callback("sessions")),
        Button("Archive", callback("archives")),
    )


def settings_row() -> tuple[Button, ...]:
    return (Button("Settings", callback("settings")),)
