# Legacy Python prototype architecture

> This document describes the retained Python behavioral prototype. It is not
> the production cluster contract. The Go implementation, leader policy,
> consensus boundary, and replaceable interaction adapters are documented in
> [Go architecture](go-architecture.md). New production decisions must be made
> against that document.

The project is a hub-and-agent system. The hub owns product state and user
navigation; an agent owns everything that is inherently local to a machine.

```text
Telegram adapter ─┐
Future web UI ─────┼─> Coordinator ─> HostRuntime
CLI/admin API ─────┘                     ├─> Local driver
                                        └─> RemoteRuntime ─> transport ─> agent
```

## Boundaries

| Layer | Owns | Must not own |
| --- | --- | --- |
| `domain` | hosts, sessions, per-user navigation | I/O, Telegram, tmux |
| `control` | use cases and routing by selected host | transport details |
| `runtime` | host operation contract and adapters | global navigation |
| `protocol` | versioned wire DTOs | business decisions |
| `transport` | connection lifecycle and request delivery | tmux commands |
| `agent` | command allowlist and host execution | Telegram state |
| `hub` | composition, connection registration, reconciliation | UI rendering |
| `persistence` | atomic hub-owned state | runtime execution |
| `telegram` | access control, screens, callbacks, message projection | tmux, transport, domain mutation |

Dependencies point inward: adapters depend on application contracts; domain
objects do not import adapters. A Telegram handler will call `Coordinator`, not
tmux or a WebSocket client directly.

## Navigation invariant

Navigation is two-dimensional and scoped per user:

1. `active_host_by_user[user]` selects a server.
2. `active_session_by_host[user][host]` remembers the last session there.
3. Switching hosts restores that session if it is live, otherwise the newest
   live session on that host is selected.

This preserves the current CCBot single-host behavior: a fresh user defaults to
the special `local` host. Multi-host controls can therefore be introduced as an
adapter feature flag without changing the domain flow for local-only users.

## Configurable session views

Session browsing mode is a persisted per-user preference, independent from the
actual selected host and session.

| Mode | Navigation | Session cards |
| --- | --- | --- |
| `host_first` | choose a host, then enter its session list | current and background sessions from that host, matching the existing CCBot card flow |
| `all_hosts` | open one global session pool | live sessions from every host; every item carries host name and stable host ID |

`host_first` is the default, so enabling multiple hosts does not force a new UI
on existing users. Both modes use the current CCBot ordering exactly: oldest to
newest by `(live_since_at or created_at, id)`. For a new session,
`live_since_at` equals `created_at`; after a restore it equals the restoration
timestamp. Selection and recent activity never change a session's position.
There is deliberately no sorting preference or placeholder for one in
persisted settings yet.

Choosing a session from another host atomically changes the selected host to
that session's host. The last selected session on every other host remains
remembered.

The coordinator returns UI-neutral `SessionListItem` values containing the
session, its host, selection status, and an unambiguous qualified name. Telegram
may render the host as a badge, prefix, icon, or secondary card line without
changing domain state or routing behavior.

## Safety and compatibility

- The original CCBot repository and state file are never modified.
- Legacy, schema-v1, and schema-v2 imports copy forward into schema v3 with
  `host_first` as the default view.
- The hub is the only global-state writer; Telegram and host-event mutations
  share one serialized mutation boundary.
- Agents expose an explicit command allowlist, not arbitrary shell execution.
- Remote and local execution implement the same `HostRuntime` contract.
- Wire messages carry a protocol version, request ID, and optional idempotency
  key.
- Go source modules are capped at 320 physical lines by CI. This is a hard
  guardrail, not a target; most modules should stay substantially smaller.

## Current composition

The same real `LocalRuntime` is used directly by a local-enabled hub and by a
remote host agent. `RemoteRuntime` carries the identical contract over an
authenticated WS/WSS channel. `HubConnectionSupervisor` binds and reconciles
connected channels without exposing transport details to `Coordinator`.

Host-local transcript/event production uses a locked provider registry,
incremental byte cursors, a durable event spool, and cumulative hub ACK. The
embedded local runtime goes through the same event consumer semantics as a
remote agent.

The Telegram adapter is an outer adapter over the tested coordinator/listing
interfaces. It uses deterministic short callback tokens rather than placing
arbitrary host/session IDs in Telegram's 64-byte callback field. Carrier
message ownership is an ephemeral Telegram projection and never enters domain
state. A post-commit event stream refreshes visible cards, while Telegram
failure is deliberately outside the durable agent ACK path.
