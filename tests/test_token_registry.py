from __future__ import annotations

import json

import pytest

from bria.security.token_registry import InvalidHostIdError, TokenRegistry


def test_issue_verify_rotate_and_revoke(tmp_path) -> None:
    path = tmp_path / "agent-tokens.json"
    registry = TokenRegistry(path)

    first = registry.issue("build-server")

    assert registry.verify("build-server", first.token)
    assert first.token not in path.read_text(encoding="utf-8")
    assert path.stat().st_mode & 0o777 == 0o600

    second = registry.issue("build-server")

    assert not registry.verify("build-server", first.token)
    assert registry.verify("build-server", second.token)
    assert TokenRegistry(path).verify("build-server", second.token)
    assert registry.revoke("build-server")
    assert not registry.verify("build-server", second.token)


def test_rejects_unsafe_host_id(tmp_path) -> None:
    registry = TokenRegistry(tmp_path / "tokens.json")

    with pytest.raises(InvalidHostIdError):
        registry.issue("../../escape")


def test_ignores_no_malformed_records(tmp_path) -> None:
    path = tmp_path / "tokens.json"
    path.write_text(json.dumps({"server-a": "not-an-object"}), encoding="utf-8")

    registry = TokenRegistry(path)

    assert registry.known_host_ids() == ()
