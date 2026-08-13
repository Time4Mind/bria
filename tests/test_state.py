from __future__ import annotations

import json

from bria.domain.enums import SessionViewMode
from bria.domain.host import LOCAL_HOST_ID
from bria.domain.project_state import ProjectState
from bria.persistence.json_store import JsonStateStore


def test_state_round_trip_preserves_per_host_navigation(tmp_path) -> None:
    path = tmp_path / "state.json"
    store = JsonStateStore(path, local_name="laptop")
    state = ProjectState.empty("laptop")
    state.navigation.activate_session(42, "server-a", "session-a")
    state.navigation.activate_session(42, LOCAL_HOST_ID, "session-local")
    state.preferences.set_session_view(42, SessionViewMode.ALL_HOSTS)

    store.save(state)
    loaded = store.load()

    assert loaded.navigation.selected_host(42) == LOCAL_HOST_ID
    assert loaded.navigation.selected_session_id(42, "server-a") == "session-a"
    assert loaded.preferences.session_view(42) is SessionViewMode.ALL_HOSTS
    assert path.stat().st_mode & 0o777 == 0o600


def test_store_imports_legacy_ccbot_state_as_local(tmp_path) -> None:
    path = tmp_path / "state.json"
    path.write_text(
        json.dumps(
            {
                "sessions": {
                    "abcd1234": {
                        "id": "abcd1234",
                        "name": "old session",
                        "workdir": "/work/project",
                        "state": "active",
                        "created_at": 12.0,
                    }
                },
                "active_sessions": {"7": "abcd1234"},
            }
        ),
        encoding="utf-8",
    )

    state = JsonStateStore(path, local_name="old-host").load()

    assert state.sessions["abcd1234"].host_id == LOCAL_HOST_ID
    assert state.navigation.selected_session_id(7) == "abcd1234"
    assert state.hosts[LOCAL_HOST_ID].name == "old-host"
    assert state.sessions["abcd1234"].live_since_at == 12.0
    assert state.sessions["abcd1234"].restored_at == 0.0


def test_schema_one_state_migrates_with_default_view_preferences() -> None:
    state = ProjectState.from_dict(
        {
            "schema_version": 1,
            "hosts": {},
            "sessions": {},
            "navigation": {},
        }
    )

    assert state.schema_version == 3
    assert state.preferences.session_view(42) is SessionViewMode.HOST_FIRST
