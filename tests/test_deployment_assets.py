from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def test_firewall_rules_are_ordered_and_idempotent(tmp_path: Path) -> None:
    state = tmp_path / "rules.json"
    fake = tmp_path / "iptables"
    fake.write_text(
        """#!/usr/bin/env python3
import json, os, sys
from pathlib import Path

path = Path(os.environ["FAKE_IPTABLES_STATE"])
rules = json.loads(path.read_text()) if path.exists() else []
args = sys.argv[1:]
if args and args[0] == "-w":
    args = args[1:]
command, chain = args[0], args[1]
tail = args[2:]
if command == "-S":
    for rule in rules:
        print("-A INPUT " + " ".join(rule))
    raise SystemExit(0)
if command == "-C":
    raise SystemExit(0 if tail in rules else 1)
if command == "-I":
    if tail and tail[0].isdigit():
        tail = tail[1:]
    rules.insert(0, tail)
elif command == "-D":
    rules.remove(tail)
else:
    raise SystemExit(2)
path.write_text(json.dumps(rules))
""",
        encoding="utf-8",
    )
    fake.chmod(0o700)
    environment = os.environ | {
        "PATH": f"{tmp_path}:{os.environ['PATH']}",
        "FAKE_IPTABLES_STATE": str(state),
        "BRIA_RAFT_CIDR": "192.0.2.0/24",
        "BRIA_PRIVATE_PORTS": "7946 7947 7948",
    }
    command = [str(ROOT / "deploy/systemd/bria-raft-firewall")]
    subprocess.run(command, env=environment, check=True)
    wrong_order = json.loads(state.read_text(encoding="utf-8"))
    for port in ("7946", "7947", "7948"):
        accept_marker = f"bria-loopback-{port}"
        drop_marker = (
            "bria-raft-private-only" if port == "7946" else f"bria-private-{port}"
        )
        accept_rule = next(rule for rule in wrong_order if accept_marker in rule)
        wrong_order.remove(accept_rule)
        drop = next(i for i, rule in enumerate(wrong_order) if drop_marker in rule)
        wrong_order.insert(drop + 1, accept_rule)
    state.write_text(json.dumps(wrong_order), encoding="utf-8")

    subprocess.run(command, env=environment, check=True)
    corrected = json.loads(state.read_text(encoding="utf-8"))
    subprocess.run(command, env=environment, check=True)
    assert corrected == json.loads(state.read_text(encoding="utf-8"))
    for port in ("7946", "7947", "7948"):
        accept = next(
            i for i, rule in enumerate(corrected) if f"bria-loopback-{port}" in rule
        )
        drop_marker = (
            "bria-raft-private-only" if port == "7946" else f"bria-private-{port}"
        )
        drop = next(i for i, rule in enumerate(corrected) if drop_marker in rule)
        assert accept < drop


def test_launch_agent_has_predictable_command_path() -> None:
    template = (ROOT / "deploy/launchd/com.time4mind.bria.plist").read_text(
        encoding="utf-8"
    )
    assert "/opt/homebrew/bin" in template
    assert "/usr/local/bin" in template
    assert "__USER_HOME__/.local/bin" in template


def test_android_supervisor_forwards_enrollment() -> None:
    supervisor = (ROOT / "deploy/supervisor/bria-supervisor.sh").read_text(
        encoding="utf-8"
    )
    assert "BRIA_TUNNEL_REVERSE_ENROLLMENT" in supervisor
    assert '-R "$reverse_enrollment"' in supervisor
