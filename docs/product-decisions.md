# Finalized product decisions

- Default navigation: choose a server, then use that server's last active
  session and CCBot-compatible session card.
- Optional `all_hosts`: one oldest-first pool, three session buttons per row,
  with a node badge on every item.
- New session: `all_hosts` always starts with the node selector. Inside a
  concrete node in `host_first`, the current node is reused without another
  selection step. If no valid node context exists, the selector establishes
  one. Offline nodes remain visible there and marked unavailable. The backend
  selector appears only when more than one create-capable backend exists on
  the chosen node; one backend is selected implicitly.
- After the node/backend choice, browse that node's filesystem with the
  CCBot-compatible directory grid. Selecting a directory offers its recent
  native Claude/Codex sessions for resume plus `Start fresh`. Choosing either
  immediately creates the tmux/provider runtime and opens an empty live card;
  no first user message is required.
- A session creation intent is committed before node-local provisioning. It is
  idempotently retried after leader/daemon/host restart. A persistently failed
  launch is archived as `resume_failed`, never shown as `Lost`.
- Session names are AI-generated through the lightest available provider model
  and remain renameable.
- Live ordering: oldest-first by current live start timestamp. Archive ordering:
  newest-first by archive timestamp.
- Idle archive: 6, 12, 24 hours, or unlimited; default 6.
- Archive retention: 14, 30 days, or unlimited; default 14. Expiry may remove
  the record only or all Bria-managed archive data.
- Daemon restart reattaches tmux. After an OS reboot, a live session is resumed
  on its origin node when its existing idle deadline has not passed; unlimited
  sessions are always attempted. The original activity and live-start clocks
  are preserved. An expired session is archived as `idle`, and a missing
  provider binding or failed resume is archived as `resume_failed`.
- `Lost` is a node-availability presentation: sessions appear there only while
  their origin node is unavailable. A failed or externally closed session on
  an online node is archived, never moved into `Lost`.
- Bria currently has exactly one trusted Telegram owner. Sessions are private,
  every enrolled node is granted to that owner automatically, unknown users
  are silently ignored, and all sharing/multi-user entry points are blocked.
  A later multi-user design requires a separate security review.
- The live-card lifecycle action is named `Close`, not `Kill`. Closing commits
  the native archive before stopping the local runtime. `Stop` only interrupts
  the current turn; after a confirmed interrupt the card exposes `Close`.
- There is no separate terminal screenshot button. When enabled, the current
  tmux pane is rendered to PNG and inserted as the card's embedded Rich
  Markdown photo block, following CCBot's terminal-card transport with a
  text-only fallback.
- Card settings independently control tool calls, tool results, reasoning
  blocks, and terminal snapshots. Hidden technical events leave no heading,
  spoiler, or placeholder; user and assistant conversation text remains.
  Terminal snapshots have their own working-only, always, or never policy and
  are not coupled to the other technical categories.
- Response cards have three modes: retain with pagination (default), retain
  with only the latest page visible on the latest card, or keep only the latest
  card with pagination. The no-pagination mode never expands the latest card
  to the full transcript.
- Provider JSONL stays on the origin node. The current leader may request a
  bounded structured view over mTLS only after the target rechecks membership,
  leadership, actor access, session identity, and runtime generation. No
  caller-supplied transcript path is accepted and no raw transcript enters
  Raft state.
- Clearing a live session interrupts the current turn, clears provider context,
  resets its generated name and provider binding, and schedules naming again
  from the next substantive prompt.
- If a user has any visible live sessions, exactly one is active. When the
  active session disappears, the most recently active eligible background
  session becomes active automatically.
- The active card carries a sticky background-status panel. In `host_first`
  it contains only sessions from the active node; in `all_hosts` it contains
  the whole visible cluster and labels every row with its node. Working,
  finished, failed, and action-required states are replicated, while provider
  output remains node-local.
- Background completion, error, and action-required pushes have independent
  per-user switches and default to enabled. A status is acknowledged only by
  switching into that session; the configurable dismissal threshold is 1, 3,
  5, or 10 such switches and defaults to 1. Leaving a session that is still
  running starts its working-status counter again; other dismissed states need
  a new runtime event before they return.
- Input received while an archived session is being restored is pinned to that
  session and held in its ordered card queue until the origin runtime is ready.
  `Lost` remains reserved for an unavailable origin node.
- Text, voice, photo, and document updates are pinned to the active session at
  receipt and share one host-local durable FIFO. The leader forwards only the
  Telegram file descriptor; the session's node downloads it directly. Voice
  is transcribed on that node without blocking Telegram. Whisper.cpp is the
  portable default; macOS Apple Silicon may select Apple Speech in mandatory
  on-device mode. It never silently falls back to Apple's network service.
- Photos and documents are stored in `<workdir>/.bria-inbox` and referenced by
  relative path. The inbox is included in the bounded native archive and
  restored without overwriting a different existing file.
- Forward origin, bot origin, captions, hidden links, and recoverable Telegram
  quotes are preserved. Content Telegram omits entirely (notably a protected
  message that cannot be forwarded) cannot be reconstructed by the Bot API.
- The owner can be changed only by a confirmation-bearing local CLI command on
  the bootstrap host; the replicated transfer removes the old identity.
- Telegram works only in private DM.
- A node must be `Disabled` before it can be deleted. Disabling asks for
  confirmation when unfinished sessions exist, attempts to close and archive
  all of them, and still completes with an explicit error summary if some
  closures fail. The daemon remains in restricted maintenance mode so the same
  identity can be enabled again remotely. The final available node cannot be
  disabled from Telegram.
- Deleting a disabled node removes membership and UI metadata, revokes its
  identity with a replicated tombstone, and requires fresh enrollment before
  that host can return. Bria never remotely erases host-local data as part of
  cluster deletion.
- Node enrollment is initiated and approved in Telegram. A one-use invitation
  valid for 30 minutes is the primary deployment flow; the node generates its
  own private key and receives a CA-signed certificate only after approval. A
  signed node contract is the alternative two-step flow. Pending requests
  remain available for 24 hours. Tokens alone never authorize membership.
- Duplicate display names receive an increasing numeric suffix automatically.
  Node names remain editable from node settings.
- Manual leadership transfer lasts up to 30 minutes or until the preferred node
  becomes unavailable.
- Menu → Status always opens in `Select` mode. Its Rich Markdown table contains
  every server/backend pair, cached quota values, reset time and age in minutes;
  offline rows retain their last value and carry an unavailable marker. Refresh
  polls nodes asynchronously. `Leader` confirms a 30-minute preference and
  `Settings` opens node details. Server sorting is globally configurable as
  creation time (default), name, or leader first; quota polling is 5 or 10
  minutes (default 10).
- Quota alerts use the CCBot thresholds 50/75/90%. Snapshots with the same
  backend and stable provider account ID are treated as one account even when
  several nodes report them. For a provider without a stable ID, the owner may
  assign the same account alias in each node's settings; matching aliases are
  then treated as one account. Without an alias, snapshots remain separate per
  node.
- Provider sign-in is initiated from a selected node's settings. Claude uses
  the same live PTY paste-code exchange as CCBot and best-effort deletes the
  one-time code message; Codex uses app-server device-code login and completes
  automatically. The target node rechecks the current Raft leader, sole owner,
  node lifecycle and advertised backend on every auth operation. Credential
  files, child processes, codes and raw CLI output remain node-local and are
  never replicated.
- Safe automatic failover uses one node or an odd quorum (normally three). Two
  connected nodes may operate, but one survivor cannot self-elect; the phone
  node is the recommended third full voting/runtime node.
- Every running node watches Raft's authenticated leader identity locally. A
  15-second loss emits one short owner notification for that node; a later
  leader observation emits one recovery notification. These messages bypass
  leader-gated update polling but never write state or grant leadership, so a
  network partition cannot turn the alert path into a second control plane.
- Updates are administrator-initiated and rolling. Recovery bundles are
  deferred.
- WireGuard is not required. Bria must not change global routes, DNS,
  firewall, or proxy settings; transport remains replaceable.
- Initial platforms: Linux AMD64/ARM64 and macOS Apple Silicon. Native Windows
  10/11 is a later full-parity update; macOS Intel is out of scope.
