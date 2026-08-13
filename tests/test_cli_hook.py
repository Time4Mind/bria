from __future__ import annotations

import io
import json

from bria import cli_hook
from bria.config import Settings


class BindingServiceSpy:
    def __init__(self) -> None:
        self.arguments: dict[str, object] = {}

    def bind(self, **arguments: object) -> object:
        self.arguments = arguments
        return object()


def test_hook_ingest_binds_stdin_payload_to_current_tmux_window(
    monkeypatch, tmp_path
) -> None:
    service = BindingServiceSpy()
    monkeypatch.setattr(cli_hook, "_service", lambda settings: service)
    monkeypatch.setattr(cli_hook, "_window_id_for_pane", lambda pane: "@7")
    payload = {
        "session_id": "12345678-1234-4234-8234-123456789abc",
        "transcript_path": "/home/agent/.claude/projects/x/session.jsonl",
    }

    result = cli_hook.ingest_hook(
        Settings(tmp_path, "server-a", "Server A", "", ""),
        backend="claude",
        source=io.StringIO(json.dumps(payload)),
        environment={"TMUX_PANE": "%3"},
    )

    assert result == 0
    assert service.arguments == {
        "backend": "claude",
        "provider_session_id": payload["session_id"],
        "transcript_path": payload["transcript_path"],
        "window_id": "@7",
    }


def test_hook_ingest_never_blocks_provider_on_invalid_input(
    monkeypatch, tmp_path
) -> None:
    service = BindingServiceSpy()
    monkeypatch.setattr(cli_hook, "_service", lambda settings: service)

    result = cli_hook.ingest_hook(
        Settings(tmp_path, "server-a", "Server A", "", ""),
        backend="claude",
        source=io.StringIO("not-json"),
        environment={},
    )

    assert result == 0
    assert service.arguments == {}
