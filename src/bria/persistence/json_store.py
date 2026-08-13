from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path
from typing import Any

from ..domain.project_state import ProjectState


class JsonStateStore:
    """Atomic JSON persistence owned by the hub."""

    def __init__(self, path: Path, *, local_name: str | None = None) -> None:
        self.path = path
        self.local_name = local_name

    def load(self) -> ProjectState:
        if not self.path.exists():
            return ProjectState.empty(self.local_name)
        data = json.loads(self.path.read_text(encoding="utf-8"))
        if not isinstance(data, dict):
            raise ValueError("state root must be an object")
        if "schema_version" not in data and "active_sessions" in data:
            return ProjectState.from_legacy_ccbot(data, local_name=self.local_name)
        return ProjectState.from_dict(data)

    def save(self, state: ProjectState) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        payload: dict[str, Any] = state.to_dict()
        encoded = json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True)
        fd, temp_name = tempfile.mkstemp(
            prefix=f".{self.path.name}.", dir=self.path.parent, text=True
        )
        temp_path = Path(temp_name)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write(encoded)
                handle.write("\n")
                handle.flush()
                os.fsync(handle.fileno())
            os.chmod(temp_path, 0o600)
            os.replace(temp_path, self.path)
        finally:
            temp_path.unlink(missing_ok=True)

