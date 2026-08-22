import importlib
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
architecture = importlib.import_module("scripts.check_architecture")
_check_go_packages = architecture._check_go_packages
_decode_json_stream = architecture._decode_json_stream

MODULE = "example.com/bria"


def go_package(
    path: str, *imports: str, name: str | None = None
) -> dict[str, object]:
    package: dict[str, object] = {
        "ImportPath": f"{MODULE}/{path}",
        "Imports": [f"{MODULE}/{dependency}" for dependency in imports],
    }
    if name is not None:
        package["Name"] = name
    return package


def test_go_architecture_accepts_forward_dependencies() -> None:
    packages = [
        go_package("internal/domain"),
        go_package("internal/application", "internal/domain"),
        go_package("internal/telegramui", "internal/domain", "internal/i18n"),
        go_package(
            "internal/telegramview",
            "internal/application",
            "internal/domain",
            "internal/i18n",
            "internal/telegramui",
        ),
        go_package("internal/telegramapp", "internal/telegramview"),
        go_package("cmd/bria", "internal/telegramapp"),
    ]

    assert _check_go_packages(packages, MODULE) == []


def test_go_architecture_rejects_reverse_generic_and_cyclic_dependencies() -> None:
    packages = [
        go_package("internal/domain", "internal/telegramapp"),
        go_package("internal/application", "internal/telegrambot"),
        go_package("internal/util"),
        go_package("internal/model", name="types"),
        go_package("internal/one", "internal/two"),
        go_package("internal/two", "internal/one"),
    ]

    failures = _check_go_packages(packages, MODULE)

    assert any("Go import cycle" in failure for failure in failures)
    assert "internal/util: generic Go package name 'util' hides ownership" in failures
    assert "internal/model: generic Go package name 'types' hides ownership" in failures
    assert "internal/domain must not depend on internal/telegramapp" in failures
    assert (
        "internal/application must not depend on internal/telegrambot" in failures
    )
    assert any("must not depend on Telegram adapter" in failure for failure in failures)


def test_go_list_json_stream_decodes_multiple_packages() -> None:
    payload = '{"ImportPath":"one","Imports":[]}\n{"ImportPath":"two"}\n'

    assert _decode_json_stream(payload) == [
        {"ImportPath": "one", "Imports": []},
        {"ImportPath": "two"},
    ]
