from __future__ import annotations

import ast
from pathlib import Path

PACKAGE = "bria"
ALLOWED_INTERNAL_DEPENDENCIES = {
    "domain": {"domain"},
    "protocol": {"domain", "protocol"},
    "transport": {"protocol", "transport"},
    "persistence": {"domain", "persistence"},
}
DISCOURAGED_MODULE_NAMES = {"common.py", "helpers.py", "utils.py"}


def main() -> int:
    package_root = Path("src") / PACKAGE
    failures: list[str] = []
    for path in sorted(package_root.rglob("*.py")):
        if path.name in DISCOURAGED_MODULE_NAMES:
            failures.append(f"{path}: generic module name hides ownership")
        layer = path.relative_to(package_root).parts[0]
        allowed = ALLOWED_INTERNAL_DEPENDENCIES.get(layer)
        if allowed is None:
            continue
        failures.extend(_check_imports(path, layer, allowed))

    if failures:
        print("\n".join(failures))
        return 1
    print("architecture boundaries: ok")
    return 0


def _check_imports(path: Path, layer: str, allowed: set[str]) -> list[str]:
    failures: list[str] = []
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in ast.walk(tree):
        dependency = _dependency_layer(node, layer)
        if dependency is not None and dependency not in allowed:
            failures.append(
                f"{path}:{node.lineno}: {layer} must not depend on {dependency}"
            )
    return failures


def _dependency_layer(node: ast.AST, current_layer: str) -> str | None:
    if isinstance(node, ast.Import):
        for alias in node.names:
            parts = alias.name.split(".")
            if parts[0] == PACKAGE and len(parts) > 1:
                return parts[1]
        return None
    if not isinstance(node, ast.ImportFrom):
        return None
    if node.level == 1:
        return current_layer
    if node.level >= 2:
        return (node.module or "").split(".", maxsplit=1)[0]
    parts = (node.module or "").split(".")
    if parts[0] == PACKAGE and len(parts) > 1:
        return parts[1]
    return None


if __name__ == "__main__":
    raise SystemExit(main())
