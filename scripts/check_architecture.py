from __future__ import annotations

import ast
import json
import os
import subprocess
from pathlib import Path
from typing import Any

PACKAGE = "bria"
ALLOWED_INTERNAL_DEPENDENCIES = {
    "domain": {"domain"},
    "protocol": {"domain", "protocol"},
    "transport": {"protocol", "transport"},
    "persistence": {"domain", "persistence"},
}
DISCOURAGED_MODULE_NAMES = {"common.py", "helpers.py", "utils.py"}
DISCOURAGED_GO_PACKAGE_NAMES = {"api", "common", "interfaces", "types", "util"}

GO_BOUNDARY_IMPORTS = {
    "internal/domain": set(),
    "internal/application": {
        "internal/clusterstate",
        "internal/domain",
        "internal/security",
    },
    "internal/callbacktoken": {
        "internal/domain",
        "internal/telegramui",
    },
    "internal/telegramui": {
        "internal/domain",
        "internal/i18n",
    },
    "internal/telegramview": {
        "internal/application",
        "internal/domain",
        "internal/i18n",
        "internal/telegramui",
    },
    "internal/telegrambot": {
        "internal/processlog",
        "internal/telegramui",
    },
    "internal/telegramoutbound": {
        "internal/processlog",
        "internal/telegrambot",
        "internal/telegramui",
    },
}
TELEGRAM_ADAPTER_PACKAGES = {
    "internal/callbacktoken",
    "internal/telegramapp",
    "internal/telegrambot",
    "internal/telegramoutbound",
    "internal/telegramui",
    "internal/telegramview",
}


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

    module = _go_module(Path("go.mod"))
    packages, list_failure = _load_go_packages()
    if list_failure is not None:
        failures.append(list_failure)
    else:
        failures.extend(_check_go_packages(packages, module))

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
                f"{path}:{getattr(node, 'lineno', 0)}: "
                f"{layer} must not depend on {dependency}"
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


def _go_module(path: Path) -> str:
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.startswith("module "):
            module = line.removeprefix("module ").strip()
            if module:
                return module
    raise ValueError(f"{path}: Go module declaration is missing")


def _load_go_packages() -> tuple[list[dict[str, Any]], str | None]:
    environment = os.environ.copy()
    try:
        result = subprocess.run(
            ["go", "list", "-json", "./..."],
            check=False,
            capture_output=True,
            env=environment,
            text=True,
        )
    except OSError as error:
        return [], f"go list could not start: {error}"
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or "unknown error"
        return [], f"go list failed; import graph may contain a cycle: {detail}"
    try:
        return _decode_json_stream(result.stdout), None
    except (json.JSONDecodeError, ValueError) as error:
        return [], f"go list returned invalid JSON: {error}"


def _decode_json_stream(payload: str) -> list[dict[str, Any]]:
    decoder = json.JSONDecoder()
    packages: list[dict[str, Any]] = []
    offset = 0
    while True:
        while offset < len(payload) and payload[offset].isspace():
            offset += 1
        if offset >= len(payload):
            return packages
        item, offset = decoder.raw_decode(payload, offset)
        if not isinstance(item, dict):
            raise ValueError("package entry must be an object")
        packages.append(item)


def _check_go_packages(packages: list[dict[str, Any]], module: str) -> list[str]:
    graph: dict[str, set[str]] = {}
    names: dict[str, str] = {}
    for package in packages:
        import_path = package.get("ImportPath")
        if not isinstance(import_path, str) or (
            import_path != module and not import_path.startswith(module + "/")
        ):
            continue
        dependencies = {
            dependency
            for dependency in package.get("Imports", [])
            if isinstance(dependency, str) and dependency.startswith(module + "/")
        }
        graph[import_path] = dependencies
        name = package.get("Name")
        if isinstance(name, str):
            names[import_path] = name

    failures = _go_cycle_failures(graph)
    for import_path, dependencies in sorted(graph.items()):
        package = _relative_go_package(import_path, module)
        generic = DISCOURAGED_GO_PACKAGE_NAMES.intersection(package.split("/"))
        if names.get(import_path) in DISCOURAGED_GO_PACKAGE_NAMES:
            generic.add(names[import_path])
        for name in sorted(generic):
            failures.append(
                f"{package}: generic Go package name {name!r} hides ownership"
            )

        internal_dependencies = {
            _relative_go_package(dependency, module)
            for dependency in dependencies
            if dependency.startswith(module + "/internal/")
        }
        allowed = GO_BOUNDARY_IMPORTS.get(package)
        if allowed is not None:
            for dependency in sorted(internal_dependencies - allowed):
                failures.append(f"{package} must not depend on {dependency}")

        if package.startswith("internal/") and package not in TELEGRAM_ADAPTER_PACKAGES:
            for dependency in sorted(internal_dependencies & TELEGRAM_ADAPTER_PACKAGES):
                failures.append(
                    f"{package} must not depend on Telegram adapter {dependency}"
                )
        if package != "cmd/bria" and module + "/cmd/bria" in dependencies:
            failures.append(f"{package} must not depend on composition root cmd/bria")
    return failures


def _relative_go_package(import_path: str, module: str) -> str:
    if import_path == module:
        return "."
    return import_path.removeprefix(module + "/")


def _go_cycle_failures(graph: dict[str, set[str]]) -> list[str]:
    visiting: set[str] = set()
    visited: set[str] = set()
    stack: list[str] = []
    failures: list[str] = []

    def visit(package: str) -> None:
        if package in visited:
            return
        if package in visiting:
            start = stack.index(package)
            cycle = stack[start:] + [package]
            failures.append("Go import cycle: " + " -> ".join(cycle))
            return
        visiting.add(package)
        stack.append(package)
        for dependency in sorted(graph.get(package, set())):
            if dependency in graph:
                visit(dependency)
        stack.pop()
        visiting.remove(package)
        visited.add(package)

    for package in sorted(graph):
        visit(package)
    return failures


if __name__ == "__main__":
    raise SystemExit(main())
