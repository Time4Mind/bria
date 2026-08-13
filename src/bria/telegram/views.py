from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class Button:
    label: str
    callback_data: str


@dataclass(frozen=True, slots=True)
class Screen:
    text: str
    keyboard: tuple[tuple[Button, ...], ...] = ()
    screen_name: str = ""
    session_id: str = ""
