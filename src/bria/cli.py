from __future__ import annotations

import argparse
import asyncio
import json
import os
from pathlib import Path

from . import __version__
from .cli_tokens import issue_token, list_token_hosts, revoke_token
from .config import Settings
from .domain.project_state import ProjectState
from .hub.service import HubService
from .logging_setup import configure_logging
from .persistence.json_store import JsonStateStore
from .security.token_registry import TokenRegistry


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="bria",
        description="Bria multi-host coding-session control plane",
    )
    parser.add_argument("--version", action="version", version=__version__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser("doctor", help="validate configuration and persisted state")
    hub = subparsers.add_parser("hub", help="run or inspect the hub")
    hub_commands = hub.add_subparsers(dest="hub_command")
    hub_commands.add_parser("run", help="run the hub until stopped")
    agent = subparsers.add_parser("agent", help="run or inspect a host agent")
    agent_commands = agent.add_subparsers(dest="agent_command")
    agent_commands.add_parser("run", help="run the agent with reconnect")

    state = subparsers.add_parser("state", help="inspect or import hub state")
    state_commands = state.add_subparsers(dest="state_command", required=True)
    state_commands.add_parser("show", help="print normalized state as JSON")
    import_legacy = state_commands.add_parser(
        "import-legacy", help="import a copy of an existing CCBot state file"
    )
    import_legacy.add_argument("source", type=Path)

    tokens = subparsers.add_parser("tokens", help="manage host-agent credentials")
    token_commands = tokens.add_subparsers(dest="token_command", required=True)
    issue = token_commands.add_parser("issue", help="issue or rotate a host token")
    issue.add_argument("host_id")
    revoke = token_commands.add_parser("revoke", help="revoke a host token")
    revoke.add_argument("host_id")
    token_commands.add_parser("list", help="list hosts with issued tokens")

    hook = subparsers.add_parser("hook", help="bind provider lifecycle events")
    hook_commands = hook.add_subparsers(dest="hook_command", required=True)
    bind = hook_commands.add_parser("bind", help="bind one known local session")
    bind.add_argument("--backend", choices=("claude", "codex"), required=True)
    bind.add_argument("--provider-session-id", required=True)
    bind.add_argument("--transcript-path", type=Path, required=True)
    target = bind.add_mutually_exclusive_group(required=True)
    target.add_argument("--session-id", default="")
    target.add_argument("--window-id", default="")
    ingest = hook_commands.add_parser(
        "ingest", help="consume one provider hook JSON object from stdin"
    )
    ingest.add_argument(
        "--backend",
        choices=("claude", "codex"),
        default=None,
    )
    install = hook_commands.add_parser(
        "install", help="idempotently install provider lifecycle hooks"
    )
    install.add_argument("--backend", choices=("claude", "codex"), required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    settings = Settings.from_env()
    if args.command == "doctor":
        return _doctor(settings)
    if args.command == "hub":
        if args.hub_command == "run":
            from .hub.runner import run_hub

            configure_logging()
            return asyncio.run(run_hub(settings))
        return _hub(settings)
    if args.command == "agent":
        if args.agent_command == "run":
            from .agent.runner import run_agent

            configure_logging()
            return asyncio.run(run_agent(settings))
        return _agent(settings)
    if args.command == "state" and args.state_command == "show":
        return _show_state(settings)
    if args.command == "state" and args.state_command == "import-legacy":
        return _import_legacy(settings, args.source)
    if args.command == "tokens" and args.token_command == "issue":
        return issue_token(settings, args.host_id)
    if args.command == "tokens" and args.token_command == "revoke":
        return revoke_token(settings, args.host_id)
    if args.command == "tokens" and args.token_command == "list":
        return list_token_hosts(settings)
    if args.command == "hook":
        from .cli_hook import bind_provider, ingest_hook

        if args.hook_command == "bind":
            return bind_provider(
                settings,
                backend=args.backend,
                provider_session_id=args.provider_session_id,
                transcript_path=args.transcript_path,
                session_id=args.session_id,
                window_id=args.window_id,
            )
        if args.hook_command == "install":
            from .runtime.hook_installer import HookInstaller

            path, added = HookInstaller().install(args.backend)
            print(f"hook settings: {path} ({added} event entries added)")
            return 0
        backend = args.backend or os.getenv(
            "BRIA_AGENT_BACKEND", "claude"
        )
        return ingest_hook(settings, backend=backend)
    raise AssertionError("unreachable command")


def _doctor(settings: Settings) -> int:
    hub = HubService.load(settings)
    counts = f"{len(hub.state.hosts)} hosts, {len(hub.state.sessions)} sessions"
    print(f"state: ok ({counts})")
    print(f"data directory: {settings.data_dir}")
    print(
        "remote host credentials: "
        f"{len(TokenRegistry(settings.agent_tokens_file).known_host_ids())}"
    )
    local_status = "enabled" if settings.local_runtime_enabled else "disabled"
    print(f"local runtime: {local_status}")
    print("transport adapter: WebSocket available")
    telegram_status = "configured" if settings.telegram_token else "disabled"
    print(f"telegram adapter: {telegram_status}")
    return 0


def _hub(settings: Settings) -> int:
    hub = HubService.load(settings)
    print("hub composition: ready")
    print(f"state file: {settings.state_file}")
    print(f"known hosts: {', '.join(sorted(hub.state.hosts))}")
    print(f"network bind: {settings.hub_bind_host}:{settings.hub_port}")
    return 0


def _agent(settings: Settings) -> int:
    print(f"agent host id: {settings.host_id}")
    print(f"agent host name: {settings.host_name}")
    print(f"hub URL: {settings.hub_url or '<not configured>'}")
    token_status = "configured" if settings.agent_token else "<not configured>"
    print(f"agent token: {token_status}")
    print(f"tmux session: {settings.tmux_session_name}")
    return 0


def _show_state(settings: Settings) -> int:
    state = JsonStateStore(settings.state_file, local_name=settings.host_name).load()
    print(json.dumps(state.to_dict(), ensure_ascii=False, indent=2, sort_keys=True))
    return 0


def _import_legacy(settings: Settings, source: Path) -> int:
    if settings.state_file.exists():
        raise SystemExit(f"refusing to overwrite existing state: {settings.state_file}")
    data = json.loads(source.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise SystemExit("legacy state root must be an object")
    store = JsonStateStore(settings.state_file, local_name=settings.host_name)
    state = ProjectState.from_legacy_ccbot(data, local_name=settings.host_name)
    store.save(state)
    print(f"imported {len(state.sessions)} sessions into {settings.state_file}")
    return 0
