from __future__ import annotations

import asyncio
import contextlib
import logging

from telegram import Bot, InlineKeyboardButton, InlineKeyboardMarkup
from telegram.error import TelegramError

from ..hub.event_stream import HubEventStream
from ..protocol.messages import EventName
from .controller import TelegramController
from .projection import TelegramProjection
from .views import Screen

logger = logging.getLogger(__name__)

_CARD_EVENTS = frozenset(
    {
        EventName.SESSION_CHANGED,
        EventName.SESSION_OUTPUT,
        EventName.INTERACTIVE_PROMPT,
    }
)


class TelegramEventSink:
    def __init__(
        self,
        stream: HubEventStream,
        controller: TelegramController,
        projection: TelegramProjection,
        bot: Bot,
    ) -> None:
        self.stream = stream
        self.controller = controller
        self.projection = projection
        self.bot = bot

    async def run(self) -> None:
        async with self.stream.subscribe() as events:
            async for applied in events:
                event = applied.event
                if event.name not in _CARD_EVENTS or not event.session_id:
                    continue
                await self._refresh_cards(event.session_id)

    async def _refresh_cards(self, session_id: str) -> None:
        carriers = self.projection.cards_for_session(session_id)
        for carrier in carriers:
            try:
                screen = await self.controller.card(carrier.user_id, session_id)
                await self.bot.edit_message_text(
                    chat_id=carrier.chat_id,
                    message_id=carrier.message_id,
                    text=screen.text,
                    reply_markup=markup(screen),
                )
            except TelegramError as exc:
                if "message is not modified" not in str(exc).lower():
                    logger.warning("Telegram card refresh failed: %s", exc)
            except Exception:
                logger.exception("cannot render event-driven Telegram card")
            await asyncio.sleep(0)


def markup(screen: Screen) -> InlineKeyboardMarkup | None:
    if not screen.keyboard:
        return None
    return InlineKeyboardMarkup(
        [
            [InlineKeyboardButton(button.label, callback_data=button.callback_data)
             for button in row]
            for row in screen.keyboard
        ]
    )


async def stop_task(task: asyncio.Task[None] | None) -> None:
    if task is None:
        return
    task.cancel()
    with contextlib.suppress(asyncio.CancelledError):
        await task
