from __future__ import annotations

import os
import subprocess
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
INSTALLER = PROJECT_ROOT / "deploy" / "launchd" / "install-user.sh"


def install_fixture(
    tmp_path: Path,
    *,
    service_running: bool = True,
) -> tuple[dict[str, str], Path]:
    home = tmp_path / "home"
    data_dir = home / ".bria-standalone"
    data_dir.mkdir(parents=True)
    config = data_dir / "config.json"
    config.write_text("{}", encoding="utf-8")
    fake_bin = tmp_path / "bin"
    fake_bin.mkdir()
    counter = tmp_path / "probe-count"
    service_state = tmp_path / "service-running"
    binary = fake_bin / "bria"
    binary.write_text(
        """#!/bin/sh
if [ "$1 $2" = "node probe" ]; then
  count=0
  [ ! -r "$FAKE_PROBE_COUNT" ] || count=$(cat "$FAKE_PROBE_COUNT")
  count=$((count + 1))
  printf '%s\n' "$count" >"$FAKE_PROBE_COUNT"
  [ "$count" -ge "${FAKE_READY_AFTER:-1}" ]
fi
""",
        encoding="utf-8",
    )
    binary.chmod(0o755)
    launchctl = fake_bin / "launchctl"
    launchctl.write_text(
        """#!/bin/sh
case "$1" in
  bootout) rm -f "$FAKE_SERVICE_STATE"; exit 0 ;;
  bootstrap) : >"$FAKE_SERVICE_STATE"; exit 0 ;;
  print) [ -e "$FAKE_SERVICE_STATE" ] && [ "${FAKE_SERVICE_RUNNING:-1}" = 1 ] ;;
  *) exit 1 ;;
esac
""",
        encoding="utf-8",
    )
    launchctl.chmod(0o755)
    env = os.environ.copy()
    env.update(
        {
            "HOME": str(home),
            "PATH": f"{fake_bin}:{env['PATH']}",
            "BRIA_BINARY": str(binary),
            "BRIA_DATA_DIR": str(data_dir),
            "BRIA_CONFIG": str(config),
            "FAKE_PROBE_COUNT": str(counter),
            "FAKE_SERVICE_STATE": str(service_state),
            "FAKE_SERVICE_RUNNING": "1" if service_running else "0",
        }
    )
    return env, counter


def test_install_waits_until_node_is_ready(tmp_path: Path) -> None:
    env, counter = install_fixture(tmp_path)
    env["FAKE_READY_AFTER"] = "3"

    result = subprocess.run(
        [str(INSTALLER)], check=False, capture_output=True, text=True, env=env
    )

    assert result.returncode == 0, result.stderr
    assert counter.read_text(encoding="utf-8").strip() == "3"
    assert "installed" in result.stdout


def test_install_fails_if_service_exits_before_readiness(tmp_path: Path) -> None:
    env, counter = install_fixture(tmp_path, service_running=False)
    env["FAKE_READY_AFTER"] = "99"

    result = subprocess.run(
        [str(INSTALLER)], check=False, capture_output=True, text=True, env=env
    )

    assert result.returncode != 0
    assert counter.read_text(encoding="utf-8").strip() == "1"
    assert "exited before becoming ready" in result.stderr
