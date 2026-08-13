from __future__ import annotations

import copy

from ..domain.enums import SessionViewMode
from ..hub.service import HubService
from .callback_data import parse, resolve_token
from .render.card import render_archive_confirmation, render_card
from .render.hosts import render_home, render_hosts
from .render.sessions import render_archive, render_sessions
from .render.settings import render_settings
from .views import Screen


class TelegramController:
    """PTB-neutral adapter boundary around the serialized hub services."""

    def __init__(self, hub: HubService, *, allowed_users: frozenset[int]) -> None:
        self.hub = hub
        self.allowed_users = allowed_users

    def authorize(self, user_id: int) -> bool:
        return user_id in self.allowed_users

    async def home(self, user_id: int) -> Screen:
        del user_id
        return render_home()

    async def handle_callback(self, user_id: int, value: str) -> Screen:
        action, argument = parse(value)
        if action == "home":
            return await self.home(user_id)
        if action == "hosts":
            return await self.hosts(user_id)
        if action == "sessions":
            return await self.open_sessions(user_id)
        if action == "archives":
            return await self.archives(user_id)
        if action == "settings":
            return await self.settings(user_id)
        if action == "view":
            return await self.set_view(user_id, argument)
        if action == "host":
            return await self.select_host(user_id, argument)
        if action == "session":
            return await self.select_session(user_id, argument)
        if action == "card":
            return await self.card(user_id, await self._session_id(argument))
        if action == "archive":
            return await self.confirm_archive(user_id, argument)
        if action == "archive_do":
            return await self.archive(user_id, argument)
        if action == "restore":
            return await self.restore(user_id, argument)
        raise ValueError("unsupported callback action")

    async def hosts(self, user_id: int) -> Screen:
        async with self.hub.mutation_lock:
            listing = self.hub.coordinator.host_listing(user_id)
            return render_hosts(copy.deepcopy(listing))

    async def open_sessions(self, user_id: int) -> Screen:
        async with self.hub.mutation_lock:
            mode = self.hub.coordinator.session_view_mode(user_id)
            if mode is SessionViewMode.HOST_FIRST:
                return render_hosts(
                    copy.deepcopy(self.hub.coordinator.host_listing(user_id))
                )
            return render_sessions(
                copy.deepcopy(self.hub.coordinator.session_listing(user_id))
            )

    async def select_host(self, user_id: int, token: str) -> Screen:
        async with self.hub.mutation_lock:
            host_id = resolve_token(
                "host", token, self.hub.coordinator.known_host_ids()
            )
            self.hub.coordinator.switch_host(user_id, host_id)
            return render_sessions(
                copy.deepcopy(self.hub.coordinator.session_listing(user_id))
            )

    async def select_session(self, user_id: int, token: str) -> Screen:
        session_id = await self._session_id(token)
        async with self.hub.mutation_lock:
            self.hub.coordinator.activate_session(user_id, session_id)
        return await self.card(user_id, session_id)

    async def card(self, user_id: int, session_id: str) -> Screen:
        del user_id
        async with self.hub.mutation_lock:
            session, host = self.hub.coordinator.session_details(session_id)
            selected_session = copy.deepcopy(session)
            selected_host = copy.deepcopy(host)
        try:
            capture = await self.hub.coordinator.capture_session(session_id)
            pane = capture.text
        except Exception:
            pane = "(terminal is currently unavailable)"
        return render_card(selected_session, selected_host, pane)

    async def archives(self, user_id: int) -> Screen:
        async with self.hub.mutation_lock:
            listing = copy.deepcopy(self.hub.coordinator.archive_listing(user_id))
        return render_archive(listing)

    async def settings(self, user_id: int) -> Screen:
        async with self.hub.mutation_lock:
            mode = self.hub.coordinator.session_view_mode(user_id)
        return render_settings(mode)

    async def set_view(self, user_id: int, value: str) -> Screen:
        modes = {"host": SessionViewMode.HOST_FIRST, "all": SessionViewMode.ALL_HOSTS}
        try:
            mode = modes[value]
        except KeyError as exc:
            raise ValueError("unknown session view") from exc
        async with self.hub.mutation_lock:
            self.hub.coordinator.set_session_view_mode(user_id, mode)
        return render_settings(mode)

    async def confirm_archive(self, user_id: int, token: str) -> Screen:
        del user_id
        session_id = await self._session_id(token)
        async with self.hub.mutation_lock:
            session, host = self.hub.coordinator.session_details(session_id)
            return render_archive_confirmation(
                copy.deepcopy(session), copy.deepcopy(host)
            )

    async def archive(self, user_id: int, token: str) -> Screen:
        session_id = await self._session_id(token)
        async with self.hub.mutation_lock:
            await self.hub.coordinator.archive_session(user_id, session_id)
        return await self.home(user_id)

    async def restore(self, user_id: int, token: str) -> Screen:
        session_id = await self._session_id(token)
        async with self.hub.mutation_lock:
            await self.hub.coordinator.restore_session(user_id, session_id)
        return await self.card(user_id, session_id)

    async def send_text(self, user_id: int, text: str) -> Screen:
        async with self.hub.mutation_lock:
            selected = self.hub.coordinator.selected_session(user_id)
            if selected is None:
                raise LookupError("no active session")
            session_id = selected.id
            await self.hub.coordinator.send_text_to(session_id, text)
        return await self.card(user_id, session_id)

    async def _session_id(self, token: str) -> str:
        async with self.hub.mutation_lock:
            return resolve_token(
                "session", token, self.hub.coordinator.known_session_ids()
            )
