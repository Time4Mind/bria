from __future__ import annotations

import json
import stat
from pathlib import Path

from bria.runtime.hook_installer import HookInstaller


def test_hook_install_is_atomic_idempotent_and_preserves_settings(
    tmp_path: Path,
) -> None:
    settings = tmp_path / ".claude" / "settings.json"
    settings.parent.mkdir()
    settings.write_text(json.dumps({"theme": "dark"}), encoding="utf-8")
    settings.chmod(0o640)
    installer = HookInstaller(
        claude_settings=settings,
        codex_settings=tmp_path / ".codex" / "hooks.json",
        executable="/opt/bria/bin/bria",
    )

    first_path, first_added = installer.install("claude")
    _, second_added = installer.install("claude")
    stored = json.loads(settings.read_text(encoding="utf-8"))

    assert first_path == settings
    assert first_added == 2
    assert second_added == 0
    assert stored["theme"] == "dark"
    assert set(stored["hooks"]) == {"SessionStart", "UserPromptSubmit"}
    command = stored["hooks"]["SessionStart"][0]["hooks"][0]["command"]
    assert command == "/opt/bria/bin/bria hook ingest --backend claude"
    assert stat.S_IMODE(settings.stat().st_mode) == 0o640
