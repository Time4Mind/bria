from __future__ import annotations

import fcntl
import json
import os
import tempfile
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

from ..domain.enums import SessionState
from .base import RuntimeSession


@dataclass(slots=True)
class LocalSessionRecord:
    session_id: str
    window_id: str
    workdir: str
    name: str
    backend: str
    provider_session_id: str = ""
    transcript_path: str = ""
    state: SessionState = SessionState.ACTIVE

    def runtime_session(self) -> RuntimeSession:
        return RuntimeSession(
            session_id=self.session_id,
            window_id=self.window_id,
            workdir=self.workdir,
            name=self.name,
            backend=self.backend,
            provider_session_id=self.provider_session_id,
            state=self.state,
        )

    def to_dict(self) -> dict[str, Any]:
        result = asdict(self)
        result["state"] = self.state.value
        return result

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> LocalSessionRecord:
        return cls(
            session_id=str(data["session_id"]),
            window_id=str(data.get("window_id", "")),
            workdir=str(data.get("workdir", "")),
            name=str(data.get("name") or data["session_id"]),
            backend=str(data.get("backend", "claude")),
            provider_session_id=str(data.get("provider_session_id", "")),
            transcript_path=str(data.get("transcript_path", "")),
            state=SessionState(str(data.get("state", SessionState.ACTIVE.value))),
        )


class LocalSessionRegistry:
    """Small durable map joining Bria ids to tmux window ids."""

    def __init__(self, path: Path | None) -> None:
        self.path = path
        self.records = self._load()

    def get(self, session_id: str) -> LocalSessionRecord:
        try:
            return self.records[session_id]
        except KeyError as exc:
            raise LookupError(f"unknown local session: {session_id}") from exc

    def find_by_window(self, window_id: str) -> LocalSessionRecord:
        matches = [
            record for record in self.records.values() if record.window_id == window_id
        ]
        if len(matches) != 1:
            raise LookupError(
                f"window does not identify one local session: {window_id}"
            )
        return matches[0]

    def upsert(self, record: LocalSessionRecord) -> None:
        self.records[record.session_id] = record
        self.save()

    def save(self) -> None:
        if self.path is None:
            return
        with self._locked():
            persisted = self._read_path()
            for identifier, record in self.records.items():
                existing = persisted.get(identifier)
                if existing is not None:
                    self._preserve_binding(record, existing)
                persisted[identifier] = record
            self.records = persisted
            self._write_path()

    def bind_provider(
        self,
        record: LocalSessionRecord,
        *,
        backend: str,
        provider_session_id: str,
        transcript_path: str,
    ) -> LocalSessionRecord:
        if self.path is None:
            target = self.get(record.session_id)
            self._ensure_unique_binding(
                self.records, target, provider_session_id, transcript_path
            )
            self._apply_binding(target, backend, provider_session_id, transcript_path)
            return target
        with self._locked():
            persisted = self._read_path()
            target = persisted.get(record.session_id)
            if target is None:
                target = self.records.get(record.session_id)
            if target is None:
                raise LookupError(f"unknown local session: {record.session_id}")
            self._ensure_unique_binding(
                persisted, target, provider_session_id, transcript_path
            )
            self._apply_binding(target, backend, provider_session_id, transcript_path)
            persisted[target.session_id] = target
            self.records = persisted
            self._write_path()
            return target

    def refresh(self) -> None:
        if self.path is not None:
            with self._locked():
                self.records = self._read_path()

    @contextmanager
    def _locked(self) -> Iterator[None]:
        if self.path is None:
            yield
            return
        self.path.parent.mkdir(parents=True, exist_ok=True)
        lock_path = self.path.with_suffix(f"{self.path.suffix}.lock")
        with lock_path.open("a+", encoding="utf-8") as handle:
            os.chmod(lock_path, 0o600)
            fcntl.flock(handle, fcntl.LOCK_EX)
            try:
                yield
            finally:
                fcntl.flock(handle, fcntl.LOCK_UN)

    def _write_path(self) -> None:
        if self.path is None:
            return
        payload = {
            "schema_version": 2,
            "sessions": {key: value.to_dict() for key, value in self.records.items()},
        }
        fd, temporary_name = tempfile.mkstemp(
            dir=self.path.parent, prefix=f".{self.path.name}.", suffix=".tmp"
        )
        temporary = Path(temporary_name)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                json.dump(payload, handle, ensure_ascii=False, indent=2)
                handle.flush()
                os.fsync(handle.fileno())
            temporary.chmod(0o600)
            temporary.replace(self.path)
        finally:
            temporary.unlink(missing_ok=True)

    def _load(self) -> dict[str, LocalSessionRecord]:
        if self.path is None or not self.path.exists():
            return {}
        return self._read_path()

    def _read_path(self) -> dict[str, LocalSessionRecord]:
        if self.path is None or not self.path.exists():
            return {}
        try:
            payload = json.loads(self.path.read_text(encoding="utf-8"))
            sessions = payload.get("sessions", {})
            if not isinstance(sessions, dict):
                raise ValueError("sessions must be an object")
            return {
                str(key): LocalSessionRecord.from_dict(value)
                for key, value in sessions.items()
                if isinstance(value, dict)
            }
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            raise RuntimeError(f"cannot load local runtime registry: {exc}") from exc

    @staticmethod
    def _preserve_binding(
        record: LocalSessionRecord, persisted: LocalSessionRecord
    ) -> None:
        if persisted.provider_session_id:
            record.provider_session_id = persisted.provider_session_id
        if persisted.transcript_path:
            record.transcript_path = persisted.transcript_path

    @staticmethod
    def _apply_binding(
        record: LocalSessionRecord,
        backend: str,
        provider_session_id: str,
        transcript_path: str,
    ) -> None:
        record.backend = backend
        record.provider_session_id = provider_session_id
        record.transcript_path = transcript_path

    @staticmethod
    def _ensure_unique_binding(
        records: dict[str, LocalSessionRecord],
        target: LocalSessionRecord,
        provider_session_id: str,
        transcript_path: str,
    ) -> None:
        for record in records.values():
            if record.session_id == target.session_id:
                continue
            if record.provider_session_id == provider_session_id:
                raise ValueError("provider session id is already bound")
            if record.transcript_path == transcript_path:
                raise ValueError("transcript path is already bound")
