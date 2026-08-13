from __future__ import annotations

from collections.abc import Iterable

from .base import HostRuntime


class RuntimeNotFoundError(LookupError):
    pass


class RuntimeRegistry:
    """Maps stable host IDs to connected local or remote runtime adapters."""

    def __init__(self, runtimes: Iterable[HostRuntime] = ()) -> None:
        self._runtimes: dict[str, HostRuntime] = {}
        for runtime in runtimes:
            self.register(runtime)

    def register(self, runtime: HostRuntime) -> None:
        if runtime.host_id in self._runtimes:
            raise ValueError(f"runtime already registered: {runtime.host_id}")
        self._runtimes[runtime.host_id] = runtime

    def replace(self, runtime: HostRuntime) -> None:
        self._runtimes[runtime.host_id] = runtime

    def unregister(self, host_id: str) -> HostRuntime | None:
        return self._runtimes.pop(host_id, None)

    def get(self, host_id: str) -> HostRuntime:
        try:
            return self._runtimes[host_id]
        except KeyError as exc:
            raise RuntimeNotFoundError(f"host runtime unavailable: {host_id}") from exc

    def connected_host_ids(self) -> tuple[str, ...]:
        return tuple(sorted(self._runtimes))

