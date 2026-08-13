# Roadmap

## Completed foundation

- Real tmux runtime for Claude/Codex create, resume, input, capture, archive,
  restore, directories, uploads, and reconciliation.
- Schema-v3 archive timestamps and host-aware live/archive listings.
- Authenticated outbound WS/WSS transport, protocol limits, correlation, TLS
  contexts, duplicate-host protection, and agent reconnect backoff.
- Hashed host-token issue/rotate/revoke flow.
- Executable headless hub and agent processes.
- Legacy CCBot state import and local/remote runtime parity tests.
- Sequenced durable host events, cumulative ACK/replay, deduplication, and a
  post-commit hub event stream.
- Claude/Codex lifecycle hook install/ingest, provider binding, and incremental
  transcript activity monitoring.
- Initial Telegram slice: access allowlist, servers, both session views,
  terminal card/input, settings toggle, archive, restore, and live refresh.
- systemd service templates.
- Telegram-approved invitation and signed-contract enrollment, dynamic Raft
  membership, and disable/enable/delete node lifecycle.

## 1. Rich transcript and provider parity

- Normalize Claude/Codex transcript records into rich card events.
- Completed: reconcile externally removed tmux windows during normal daemon
  operation, with consecutive confirmation, optimistic generation/revision
  guards, native archive creation, and no misuse of `Lost`.
- Completed: reconcile missed provider completion events after a full Bria
  outage through bounded heartbeat evidence (session, generation, timestamp,
  digest); raw transcript text remains origin-local.
- Completed: node-local Claude paste-code and Codex device-code authentication
  driven from Telegram over the current-leader mTLS control plane. CLI
  processes and credential stores stay on the selected node; auth codes and
  provider output never enter Raft. Usage/quota polling is also complete.

## 2. Telegram product adapter

- Completed: CCBot-compatible cards, history, background status, interactive
  keys, uploads, embedded screenshots, provider auth, and quota UI.
- Completed: mandatory node selection, capability-aware backend selection,
  remote directory picker, native Claude/Codex resume picker, immediate empty
  card, restart-safe provider provisioning, ordered text/voice/photo/document
  intake, target-node Telegram download, local whisper.cpp or macOS Apple
  Speech transcription, archived session inboxes, embedded pane screenshots,
  automatic interactive prompt keyboards with background alerts, and native
  archive transcript inspection with separate History navigation. Sticky,
  mode-aware background status panels and completion/error/action pushes are
  also complete with replicated acknowledgement state. Live cards include
  bounded transcript pagination, and host-first node selection opens the last
  active card directly.
- Completed: manual account aliases for providers that expose no stable account
  ID, with replicated per-node configuration and alias-based cluster quota
  deduplication. Automatic deduplication by stable account ID remains primary.
- Completed: pagination for server, live-session, and archive collections with
  actor-bound callbacks in node-scoped and all-host views.
- Completed: offline/reconnecting live cards fail closed to read-only controls.
- Completed: host-first directly opens the current card/list when only one
  enabled node exists.

## 3. Reliability and operations

- Completed: node-generated certificate renewal with proof of the current
  identity, CA-side approval checks, atomic versioned installation, verified
  rollback, replicated key-fingerprint pinning, transition-safe key rotation,
  and disable/delete revocation across node-control and Raft transport.
- Completed: quorum-preserving rolling-update and external canary runbooks.
- Completed: authenticated process health and Raft readiness endpoints, a
  supervisor-friendly CLI probe, partition log rate limiting, and opt-in
  process-level failover/quorum chaos tests using TCP, mTLS, and disk state.
- Completed: bounded Prometheus process/readiness/Raft metrics on the existing
  mTLS member-only control endpoint, with no session or provider data.
- Completed: member-authenticated, leader-signed logical backups with checksum,
  dry-run inspection, fresh-Raft staging, one-shot idempotent restore, and a
  process-level retain/stage/restart/re-backup recovery test.
- Multi-process failure tests: reconnect, duplicate connection, hub restart,
  partial archive/restore, and protocol version skew. Completed: archive
  commit/deactivation crash reconciliation and fail-closed command/snapshot
  version-boundary tests.

## 4. Rollout gates

- Automated local and process-level parity/chaos canaries are complete.
- A real three-host deployment and owner-visible Telegram acceptance are
  operator gates, not implementation work; synthetic Bot API E2E stays last.
- `host_first` remains the safe default; `all_hosts` is opt-in.

Each stage remains independently testable and reversible.
