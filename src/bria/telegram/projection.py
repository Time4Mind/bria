from __future__ import annotations

from dataclasses import dataclass

from .views import Screen


@dataclass(frozen=True, slots=True)
class TelegramCarrier:
    user_id: int
    chat_id: int
    message_id: int
    screen_name: str
    session_id: str = ""


class TelegramProjection:
    """Ephemeral Telegram message ownership, separate from domain state."""

    def __init__(self) -> None:
        self._carriers: dict[int, TelegramCarrier] = {}

    def record(
        self, user_id: int, chat_id: int, message_id: int, screen: Screen
    ) -> None:
        self._carriers[user_id] = TelegramCarrier(
            user_id=user_id,
            chat_id=chat_id,
            message_id=message_id,
            screen_name=screen.screen_name,
            session_id=screen.session_id,
        )

    def cards_for_session(self, session_id: str) -> tuple[TelegramCarrier, ...]:
        return tuple(
            carrier
            for carrier in self._carriers.values()
            if carrier.screen_name == "card" and carrier.session_id == session_id
        )
