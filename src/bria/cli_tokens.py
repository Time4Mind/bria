from __future__ import annotations

from .config import Settings
from .security.token_registry import TokenRegistry


def issue_token(settings: Settings, host_id: str) -> int:
    issued = TokenRegistry(settings.agent_tokens_file).issue(host_id)
    print(f"host: {issued.host_id}")
    print(f"token: {issued.token}")
    print("Store this token now; Bria keeps only its digest.")
    return 0


def revoke_token(settings: Settings, host_id: str) -> int:
    removed = TokenRegistry(settings.agent_tokens_file).revoke(host_id)
    if not removed:
        print(f"no token registered for host: {host_id}")
        return 1
    print(f"revoked token for host: {host_id}")
    return 0


def list_token_hosts(settings: Settings) -> int:
    registry = TokenRegistry(settings.agent_tokens_file)
    for host_id in registry.known_host_ids():
        print(host_id)
    return 0
