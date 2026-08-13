from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import Protocol


@dataclass(frozen=True, slots=True)
class CommandResult:
    returncode: int
    stdout: str = ""
    stderr: str = ""


class CommandRunner(Protocol):
    async def run(
        self,
        argv: tuple[str, ...],
        *,
        input_bytes: bytes | None = None,
    ) -> CommandResult: ...


class SubprocessRunner:
    """Run fixed argv commands without involving a shell."""

    def __init__(self, timeout: float = 15.0) -> None:
        self.timeout = timeout

    async def run(
        self,
        argv: tuple[str, ...],
        *,
        input_bytes: bytes | None = None,
    ) -> CommandResult:
        process = await asyncio.create_subprocess_exec(
            *argv,
            stdin=asyncio.subprocess.PIPE if input_bytes is not None else None,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        try:
            stdout, stderr = await asyncio.wait_for(
                process.communicate(input_bytes), timeout=self.timeout
            )
        except TimeoutError:
            process.kill()
            await process.wait()
            raise
        return CommandResult(
            returncode=process.returncode or 0,
            stdout=stdout.decode("utf-8", errors="replace"),
            stderr=stderr.decode("utf-8", errors="replace"),
        )


class LocalCommandError(RuntimeError):
    def __init__(self, argv: tuple[str, ...], result: CommandResult) -> None:
        detail = result.stderr.strip() or result.stdout.strip() or "command failed"
        super().__init__(f"{argv[0]} exited with {result.returncode}: {detail}")
        self.argv = argv
        self.result = result
