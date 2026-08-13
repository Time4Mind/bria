# Archive and restore

Bria preserves a session's original creation time across archive cycles.
The timestamp model is explicit:

- `created_at` is the immutable original creation timestamp;
- `live_since_at` is the start of the current live interval;
- `archived_at` is the most recent successful archive timestamp;
- `restored_at` is the most recent successful restore timestamp;
- `last_event_at` tracks activity and never affects list position.

## Ordering

Live session lists in both `host_first` and `all_hosts` modes are sorted oldest
to newest by `(live_since_at or created_at, id)`. A successful restore assigns
the same current timestamp to `restored_at`, `live_since_at`, and
`last_event_at`, so the restored session appears at the end of the live pool.

Archive lists use the same view mode as live lists. `host_first` shows records
from the selected host; `all_hosts` shows every host and includes host metadata
on every item. Records are sorted newest first by
`archived_at or last_event_at or created_at`, with the session ID as a stable
tie-breaker.

There is deliberately no sorting preference or placeholder for one.

## Runtime boundary and failures

Archive and restore always execute on the session's original `host_id`.
Bria validates the requested transition before calling the runtime and
changes persisted state only after the runtime operation succeeds. A runtime
failure therefore leaves hub state and user navigation unchanged.

After an archive, the invoking user's fallback is selected only from live
sessions on that same host. After a restore, the session becomes active for the
invoking user and the user's selected host changes to its original host.

## State migration

Schema v3 adds `live_since_at` and `restored_at`. When v1/v2 or legacy CCBot
state lacks these fields, `live_since_at` defaults to `created_at` and
`restored_at` defaults to zero. This preserves the existing ordering without
inventing historical restoration data.
