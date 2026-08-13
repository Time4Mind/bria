from __future__ import annotations

import json
import re
import uuid
from dataclasses import dataclass
from pathlib import Path

from .local_registry import LocalSessionRecord, LocalSessionRegistry
from .local_validation import backend as validate_backend
from .local_validation import session_id as validate_session_id

_WINDOW_ID = re.compile(r"^@[0-9]+$")


@dataclass(frozen=True, slots=True)
class ProviderBinding:
    session_id: str
    window_id: str
    backend: str
    provider_session_id: str
    transcript_path: str

    def event_data(self) -> dict[str, str]:
        """Payload for a protocol ``SESSION_CHANGED`` event."""
        return {
            "window_id": self.window_id,
            "backend": self.backend,
            "provider_session_id": self.provider_session_id,
            "transcript_path": self.transcript_path,
        }


class TranscriptPathPolicy:
    """Validate provider transcript paths and their embedded identity."""

    def __init__(
        self,
        *,
        claude_roots: tuple[Path, ...],
        codex_roots: tuple[Path, ...],
    ) -> None:
        self._roots = {
            "claude": self._resolve_roots(claude_roots),
            "codex": self._resolve_roots(codex_roots),
        }

    @classmethod
    def defaults(cls) -> TranscriptPathPolicy:
        home = Path.home()
        return cls(
            claude_roots=(home / ".claude" / "projects",),
            codex_roots=(home / ".codex" / "sessions",),
        )

    def validate(
        self, backend: str, provider_session_id: str, transcript_path: str | Path
    ) -> Path:
        selected_backend = validate_backend(backend)
        path = Path(transcript_path).expanduser()
        if not path.is_absolute():
            raise ValueError("transcript path must be absolute")
        try:
            resolved = path.resolve(strict=True)
        except OSError as exc:
            raise ValueError(f"transcript does not exist: {path}") from exc
        if not resolved.is_file() or resolved.suffix != ".jsonl":
            raise ValueError("transcript must be an existing JSONL file")
        if not any(
            resolved.is_relative_to(root) for root in self._roots[selected_backend]
        ):
            raise ValueError("transcript path is outside configured provider roots")
        if selected_backend == "claude":
            if resolved.stem != provider_session_id:
                raise ValueError(
                    "Claude transcript filename does not match provider id"
                )
        elif self._codex_session_id(resolved) != provider_session_id:
            raise ValueError("Codex transcript metadata does not match provider id")
        return resolved

    def allows(self, backend: str, transcript_path: str | Path) -> bool:
        selected_backend = validate_backend(backend)
        path = Path(transcript_path).expanduser()
        try:
            resolved = path.resolve(strict=True)
        except OSError:
            return False
        return resolved.is_file() and any(
            resolved.is_relative_to(root) for root in self._roots[selected_backend]
        )

    @staticmethod
    def _resolve_roots(roots: tuple[Path, ...]) -> tuple[Path, ...]:
        if not roots:
            raise ValueError("at least one provider transcript root is required")
        return tuple(path.expanduser().resolve() for path in roots)

    @staticmethod
    def _codex_session_id(path: Path) -> str:
        try:
            with path.open("r", encoding="utf-8", errors="replace") as handle:
                for _ in range(8):
                    line = handle.readline()
                    if not line:
                        break
                    try:
                        data = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if data.get("type") != "session_meta":
                        continue
                    payload = data.get("payload")
                    if isinstance(payload, dict):
                        return str(payload.get("id") or "")
        except OSError:
            return ""
        return ""


class HookBindingService:
    """CLI-neutral application service for trusted lifecycle hook payloads."""

    def __init__(
        self, registry: LocalSessionRegistry, policy: TranscriptPathPolicy
    ) -> None:
        self.registry = registry
        self.policy = policy

    def bind(
        self,
        *,
        backend: str,
        provider_session_id: str,
        transcript_path: str | Path,
        session_id: str = "",
        window_id: str = "",
    ) -> ProviderBinding:
        if bool(session_id) == bool(window_id):
            raise ValueError("provide exactly one of session_id or window_id")
        selected_backend = validate_backend(backend)
        provider_id = self._provider_uuid(provider_session_id)
        self.registry.refresh()
        record = self._resolve_record(session_id, window_id)
        if record.backend != selected_backend:
            raise ValueError(f"backend does not match local session: {record.backend}")
        validated_path = self.policy.validate(
            selected_backend, provider_id, transcript_path
        )
        bound = self.registry.bind_provider(
            record,
            backend=selected_backend,
            provider_session_id=provider_id,
            transcript_path=str(validated_path),
        )
        return ProviderBinding(
            session_id=bound.session_id,
            window_id=bound.window_id,
            backend=bound.backend,
            provider_session_id=bound.provider_session_id,
            transcript_path=bound.transcript_path,
        )

    def _resolve_record(self, session_id: str, window_id: str) -> LocalSessionRecord:
        if session_id:
            return self.registry.get(validate_session_id(session_id))
        if not _WINDOW_ID.fullmatch(window_id):
            raise ValueError("window_id must have tmux @<number> form")
        return self.registry.find_by_window(window_id)

    @staticmethod
    def _provider_uuid(value: str) -> str:
        try:
            parsed = uuid.UUID(value)
        except (ValueError, AttributeError) as exc:
            raise ValueError("provider session id must be a UUID") from exc
        canonical = str(parsed)
        if value.lower() != canonical:
            raise ValueError("provider session id must be a canonical UUID")
        return canonical
