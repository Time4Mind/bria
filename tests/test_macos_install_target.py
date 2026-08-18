from __future__ import annotations

import os
import subprocess
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
RESOLVER = PROJECT_ROOT / "scripts" / "macos-install-target.sh"


def resolve_target(
    home: Path, environment: dict[str, str] | None = None
) -> tuple[str, str]:
    command = (
        f'. "{RESOLVER}"; resolve_macos_install_target; '
        "printf '%s\\n%s\\n' \"$resolved_data_dir\" \"$resolved_config\""
    )
    env = {"HOME": str(home), "PATH": os.environ["PATH"]}
    env.update(environment or {})
    result = subprocess.run(
        ["sh", "-c", command], check=True, capture_output=True, text=True, env=env
    )
    data_dir, config = result.stdout.splitlines()
    return data_dir, config


def install_fake_plist_buddy(home: Path, profile: Path) -> Path:
    launch_agents = home / "Library" / "LaunchAgents"
    launch_agents.mkdir(parents=True)
    (launch_agents / "com.time4mind.bria.plist").write_text("fixture", encoding="utf-8")
    helper = home / "fake-plist-buddy"
    helper.write_text(
        """#!/bin/sh
case "$2" in
  'Print :ProgramArguments:1') printf '%s\\n' node ;;
  'Print :ProgramArguments:2') printf '%s\\n' run ;;
  'Print :ProgramArguments:3') printf '%s\\n' --config ;;
  'Print :ProgramArguments:4') printf '%s\\n' "$FAKE_CONFIG" ;;
  'Print :WorkingDirectory') printf '%s\\n' "$FAKE_DATA_DIR" ;;
  *) exit 1 ;;
esac
""",
        encoding="utf-8",
    )
    helper.chmod(0o755)
    return helper


def test_fresh_install_uses_single_node_bootstrap_path(tmp_path: Path) -> None:
    data_dir, config = resolve_target(tmp_path)

    assert data_dir == str(tmp_path / ".bria")
    assert config == str(tmp_path / ".bria" / "config.json")


def test_bare_reinstall_preserves_installed_profile(tmp_path: Path) -> None:
    profile = tmp_path / ".bria-standalone"
    helper = install_fake_plist_buddy(tmp_path, profile)

    data_dir, config = resolve_target(
        tmp_path,
        {
            "BRIA_PLIST_BUDDY": str(helper),
            "FAKE_DATA_DIR": str(profile),
            "FAKE_CONFIG": str(profile / "config.json"),
        },
    )

    assert data_dir == str(profile)
    assert config == str(profile / "config.json")


def test_explicit_profile_overrides_installed_profile(tmp_path: Path) -> None:
    installed = tmp_path / ".bria-standalone"
    selected = tmp_path / ".bria-cluster"
    helper = install_fake_plist_buddy(tmp_path, installed)

    data_dir, config = resolve_target(
        tmp_path,
        {
            "BRIA_PLIST_BUDDY": str(helper),
            "FAKE_DATA_DIR": str(installed),
            "FAKE_CONFIG": str(installed / "config.json"),
            "BRIA_DATA_DIR": str(selected),
            "BRIA_CONFIG": str(selected / "node.json"),
        },
    )

    assert data_dir == str(selected)
    assert config == str(selected / "node.json")
