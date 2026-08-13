from __future__ import annotations

import asyncio
import logging

from telegram import Message, Update
from telegram.error import BadRequest
from telegram.ext import (
    Application,
    CallbackQueryHandler,
    CommandHandler,
    ContextTypes,
    MessageHandler,
    filters,
)

from ..hub.service import HubService
from .controller import TelegramController
from .event_sink import TelegramEventSink, markup, stop_task
from .projection import TelegramProjection
from .views import Screen

logger = logging.getLogger(__name__)


class TelegramAdapter:
    """Run python-telegram-bot inside the hub's existing asyncio loop."""

    def __init__(self, hub: HubService, *, token: str, allowed_users: frozenset[int]):
        if not token:
            raise ValueError("Telegram token is required")
        if not allowed_users:
            raise ValueError("Telegram requires an explicit non-empty user allowlist")
        self.controller = TelegramController(hub, allowed_users=allowed_users)
        self.projection = TelegramProjection()
        self.application = Application.builder().token(token).build()
        self.event_sink = TelegramEventSink(
            hub.events,
            self.controller,
            self.projection,
            self.application.bot,
        )
        self._event_task: asyncio.Task[None] | None = None
        self._register_handlers()

    async def start(self) -> None:
        await self.application.initialize()
        await self.application.start()
        if self.application.updater is None:
            raise RuntimeError("Telegram polling updater is unavailable")
        await self.application.updater.start_polling()
        self._event_task = asyncio.create_task(
            self.event_sink.run(), name="telegram-event-sink"
        )

    async def close(self) -> None:
        await stop_task(self._event_task)
        self._event_task = None
        if self.application.updater is not None and self.application.updater.running:
            await self.application.updater.stop()
        if self.application.running:
            await self.application.stop()
        await self.application.shutdown()

    def _register_handlers(self) -> None:
        for command in ("start", "menu"):
            self.application.add_handler(CommandHandler(command, self._home))
        self.application.add_handler(CommandHandler("sessions", self._sessions))
        self.application.add_handler(CommandHandler("archive", self._archive))
        self.application.add_handler(CommandHandler("settings", self._settings))
        self.application.add_handler(CallbackQueryHandler(self._callback))
        self.application.add_handler(
            MessageHandler(filters.TEXT & ~filters.COMMAND, self._text)
        )

    async def _home(self, update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
        del context
        if not await self._authorize(update):
            return
        await self._present(update, await self.controller.home(_user_id(update)))

    async def _sessions(
        self, update: Update, context: ContextTypes.DEFAULT_TYPE
    ) -> None:
        del context
        if not await self._authorize(update):
            return
        screen = await self.controller.open_sessions(_user_id(update))
        await self._present(update, screen)

    async def _archive(
        self, update: Update, context: ContextTypes.DEFAULT_TYPE
    ) -> None:
        del context
        if not await self._authorize(update):
            return
        await self._present(update, await self.controller.archives(_user_id(update)))

    async def _settings(
        self, update: Update, context: ContextTypes.DEFAULT_TYPE
    ) -> None:
        del context
        if not await self._authorize(update):
            return
        screen = await self.controller.settings(_user_id(update))
        await self._present(update, screen)

    async def _callback(
        self, update: Update, context: ContextTypes.DEFAULT_TYPE
    ) -> None:
        del context
        query = update.callback_query
        if query is None:
            return
        if not await self._authorize(update):
            return
        await query.answer()
        try:
            screen = await self.controller.handle_callback(
                _user_id(update), query.data or ""
            )
            await self._present(update, screen)
        except (LookupError, ValueError) as exc:
            await query.answer(str(exc), show_alert=True)

    async def _text(self, update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
        del context
        if not await self._authorize(update) or update.message is None:
            return
        try:
            screen = await self.controller.send_text(
                _user_id(update), update.message.text or ""
            )
            await self._present(update, screen)
        except (LookupError, RuntimeError, ValueError) as exc:
            await update.message.reply_text(f"Cannot send: {exc}")

    async def _authorize(self, update: Update) -> bool:
        user = update.effective_user
        if user is not None and self.controller.authorize(user.id):
            return True
        if update.callback_query is not None:
            await update.callback_query.answer("Access denied", show_alert=True)
        elif update.effective_message is not None:
            await update.effective_message.reply_text("Access denied")
        return False

    async def _present(self, update: Update, screen: Screen) -> None:
        query = update.callback_query
        message: Message | None = None
        if query is not None and query.message is not None:
            try:
                edited = await query.edit_message_text(
                    screen.text, reply_markup=markup(screen)
                )
                if isinstance(edited, Message):
                    message = edited
                elif isinstance(query.message, Message):
                    message = query.message
            except BadRequest as exc:
                if "message is not modified" not in str(exc).lower():
                    raise
                if isinstance(query.message, Message):
                    message = query.message
        elif update.effective_message is not None:
            message = await update.effective_message.reply_text(
                screen.text, reply_markup=markup(screen)
            )
        if message is not None:
            self.projection.record(
                _user_id(update), message.chat_id, message.message_id, screen
            )


def _user_id(update: Update) -> int:
    if update.effective_user is None:
        raise ValueError("Telegram update has no user")
    return update.effective_user.id
