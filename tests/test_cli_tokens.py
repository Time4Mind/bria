from __future__ import annotations

from bria.cli import main
from bria.config import Settings
from bria.security.token_registry import TokenRegistry


def test_token_cli_issues_lists_and_revokes(monkeypatch, tmp_path, capsys) -> None:
    settings = Settings(tmp_path, "local", "Hub", "", "")
    monkeypatch.setattr(Settings, "from_env", classmethod(lambda cls: settings))

    assert main(["tokens", "issue", "server-a"]) == 0
    output = capsys.readouterr().out
    token = output.split("token: ", maxsplit=1)[1].splitlines()[0]
    assert TokenRegistry(settings.agent_tokens_file).verify("server-a", token)

    assert main(["tokens", "list"]) == 0
    assert capsys.readouterr().out == "server-a\n"
    assert main(["tokens", "revoke", "server-a"]) == 0
    assert "revoked token" in capsys.readouterr().out
