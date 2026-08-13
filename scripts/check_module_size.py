from __future__ import annotations

import argparse
from pathlib import Path


def source_files(root: Path, suffix: str) -> list[Path]:
    return sorted(
        path
        for path in root.rglob(f"*{suffix}")
        if not any(part.startswith(".") for part in path.parts)
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Enforce focused Python modules")
    parser.add_argument("--root", type=Path, default=Path("src"))
    parser.add_argument("--max-lines", type=int, default=320)
    parser.add_argument("--suffix", choices=(".py", ".go"), default=".py")
    args = parser.parse_args()

    failures: list[tuple[Path, int]] = []
    for path in source_files(args.root, args.suffix):
        count = len(path.read_text(encoding="utf-8").splitlines())
        if count > args.max_lines:
            failures.append((path, count))

    if not failures:
        print(f"module size: ok (maximum {args.max_lines} physical lines)")
        return 0
    for path, count in failures:
        print(f"{path}: {count} lines (maximum {args.max_lines})")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
