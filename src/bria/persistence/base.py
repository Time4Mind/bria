from __future__ import annotations

from typing import Protocol

from ..domain.project_state import ProjectState


class StateStore(Protocol):
    def load(self) -> ProjectState: ...

    def save(self, state: ProjectState) -> None: ...

