from __future__ import annotations

from ...domain.enums import SessionViewMode
from ..callback_data import callback
from ..views import Button, Screen


def render_settings(mode: SessionViewMode) -> Screen:
    host_mark = "✓ " if mode is SessionViewMode.HOST_FIRST else ""
    all_mark = "✓ " if mode is SessionViewMode.ALL_HOSTS else ""
    return Screen(
        "Session view\n\nChoose how the session switcher is organized.",
        (
            (Button(f"{host_mark}By server", callback("view", "host")),),
            (Button(f"{all_mark}All servers", callback("view", "all")),),
            (Button("Back", callback("home")),),
        ),
        screen_name="settings",
    )
