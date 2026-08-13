from __future__ import annotations

import argparse
from pathlib import Path


def source_files(root: Path, suffix: str) -> list[Path]:
    return sorted(
        path
        for path in root.rglob(f"*{suffix}")
        if not any(part.startswith(".") for part in path.parts)
    )


def load_baseline(path: Path | None) -> dict[str, int]:
    if path is None:
        return {}
    result: dict[str, int] = {}
    for number, raw_line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        name, separator, count = line.rpartition(" ")
        if not separator or not name or not count.isdigit():
            raise ValueError(f"{path}:{number}: expected PATH COUNT")
        result[name] = int(count)
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description="Enforce focused Python modules")
    parser.add_argument("--root", type=Path, default=Path("src"))
    parser.add_argument("--max-lines", type=int, default=320)
    parser.add_argument("--suffix", choices=(".py", ".go"), default=".py")
    parser.add_argument(
        "--baseline",
        type=Path,
        help="grandfather exact existing oversize modules while rejecting growth",
    )
    args = parser.parse_args()

    baseline = load_baseline(args.baseline)
    failures: list[tuple[Path, int]] = []
    for path in source_files(args.root, args.suffix):
        count = len(path.read_text(encoding="utf-8").splitlines())
        limit = max(args.max_lines, baseline.get(path.as_posix(), 0))
        if count > limit:
            failures.append((path, count))

    if not failures:
        print(f"module size: ok (maximum {args.max_lines} physical lines)")
        return 0
    for path, count in failures:
        print(f"{path}: {count} lines (maximum {args.max_lines})")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
