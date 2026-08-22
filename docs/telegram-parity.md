# Telegram UI parity contract

Bria's Telegram UI is an extension of the current CCBot interaction model,
not a redesign. The Go package `internal/telegramui` captures semantic screens,
buttons and callback actions without importing a Telegram SDK.
`internal/telegramview` projects actor-authorized state and opaque callback
tokens into those screens. The `internal/telegrambot` adapter owns Bot API
transport and private-DM parsing; `internal/telegramoutbound` owns bounded write
serialization, flood-wait suppression, newest-message ordering, and transport
timing. `internal/telegramapp` owns routing, semantic response-card transitions,
visible-screen epochs, and background lifecycle.

## Non-negotiable boundaries

- Telegram is DM-only. An update is accepted only when the chat is `private`,
  both identifiers are positive, and `chat_id == effective_user_id`. Group,
  supergroup and channel updates produce neither replies nor projections.
- Application authorization happens before rendering and again before an
  event-driven card refresh. Renderers only receive already-filtered items.
- Callback payloads contain a short semantic action and an opaque token, never
  a node ID, session ID, path or user ID. `Callback.Encode` validates the
  URL-safe alphabet and guarantees the Telegram 64-byte boundary.
- A callback token is an actor/action-scoped truncated HMAC. Resolution scans
  only entities already visible to that actor and compares tokens in constant
  time. `telegramui` does not own token generation or an entity registry.
- Button order and row placement listed below are compatibility behavior.
  Multi-node controls may only appear at the explicit extension points in this
  document; sharing controls are disabled in the single-owner product mode.

In `all_hosts`, the creation selector includes every actor-visible node;
offline nodes remain recognizable but cannot advance the flow. In
`host_first`, creation entered from a concrete node reuses that node and opens
the backend/directory step directly. Directory paths and provider session IDs
stay in a short-lived leader-local flow; Telegram callback payloads carry only
actor/action/flow-bound HMAC tokens. If leadership changes or the flow expires,
a stale click fails closed and never falls back to an implicit home directory.
Node-control browsing and provisioning use the same mTLS, membership,
current-leader, node ACL and exact-target checks as runtime input.

## CCBot parity matrix

| Surface | CCBot behavior and button grid | Bria contract | Allowed addition |
| --- | --- | --- | --- |
| Live card | `◀`, `N/M`, `▶`; then `⏹ Stop` or `✖ Close`, `🧹 Clear`, optional `🖥 Term`; session switcher; bottom row `+ new`, `≡ Menu` | Keep carrier edit-in-place behavior. Embed the bounded pane image in Rich Markdown instead of exposing a separate Shot button | Node badge in the header; inaccessible sessions are absent |
| Main Menu | Row 1: `📋 Sessions`, `🗄 Archive`; row 2: `📊 Status`, `🆕 New`; row 3: `⚙ Settings` | Exact 2/2/1 grid from `RenderMainMenu` | No Nodes dashboard and no sharing/admin button here |
| Sessions | Menu opens the active live-card/session surface; switcher is oldest-first and three sessions per row | Preserve session density and selection marker | `host_first` inserts the node picker; `all_hosts` labels every session with its node |
| New session | Directory rows, page navigation, `Up`, `Select`, `Menu`; resume picker with `Start fresh`, `Back to dirs`, `Menu` | Port without restructuring | `all_hosts` adds mandatory node choice; `host_first` reuses its current node; backend choice appears only for multiple usable backends |
| Archive list | Newest-first, six records per page, two inspect buttons per row, `◀`, page, `▶`, `← Back` | Preserve density and pagination | `all_hosts` is one global list with a node label on every item; `host_first` remains node-scoped; ACL/private-session filtering happens first |
| Archive inspect | Last transcript card page, then a separate paginated History surface | Inspect never expands the full transcript inline | Restore targets the original node; History is readable with view access |
| Settings | Category list with nested setting screens and current values | Preserve the edit-in-place category hierarchy | `Interface and language`, `Card content`, and `Archive`; session view stays under Interface |
| Auto-archive | `6h`, `12h`, `24h` choices, default `6h` | Preserve values; policy belongs to the session owner | No per-node variant |
| Status | Compact usage view with `🔄 Refresh` and Menu-style back navigation | Preserve through the Rich Markdown renderer | Refresh first; `Select` / `Settings` modes, followed by server buttons and the cluster quota table. Leader policy lives under `Settings → Cluster` |
| Provider auth | `/login`: Claude OAuth URL plus paste-back code; Codex device URL/code and automatic completion | Preserve the provider-native exchange inside the selected server's settings | mTLS current-leader routing; secrets stay on that server |
| Interactive UI | Space/up/tab, left/down/right, Esc/Ctrl-C/Enter, then `🔙 Back`, `+ new`, `≡ Menu` | Preserve | Key buttons are owner-only |
| History | `◀ Older`, `N/M`, `Newer ▶` | Preserve | Node badge; view access is sufficient |
| Background work | Sticky status panel plus transition-only pushes for ready, error, and action-required states | Preserve status glyphs and three independent notification switches | `host_first` scopes the panel to its node; `all_hosts` labels cluster-wide rows with their node |
| Sharing | Not present in CCBot | Disabled | No button or application mutation in single-owner mode |

The pre-existing Python prototype's `Servers / sessions` home screen is not a
product target. `/start` and `/menu` should land in the CCBot-compatible
live-card/Menu flow.

## Multi-node session modes

`host_first` remains the default. `Sessions` first opens a one-button-per-row
node picker showing status, selected marker and the count of sessions visible
to the actor. Selecting an available node opens its last active session directly
with the normal CCBot card and three-per-row switcher. A node with no active
session keeps the bounded empty/list fallback; an unavailable node keeps only
its explicit last-card and archive read surfaces. Back returns to the unchanged
main Menu.

`all_hosts` skips the picker. Sessions remain globally sorted by the
application using `(live_since_at or created_at, node_id, session_id)`. The UI
preserves that order, packs buttons three per row, and appends the node name to
every label. Renderers must not regroup the list by node.

Archives follow the same view preference. In `all_hosts`, Menu → Archive opens
one ACL-filtered, newest-first list across the cluster and labels each item with
its origin node. In `host_first`, it opens only the selected node's archives.
Both modes show six archives per page and two buttons per row. `Lost` sessions
never enter either archive list. Every page uses the same continuous numbering
and text layout: name, two concise description sentences, and one separator per
session. Page position is shown only by the inline keyboard; the message does
not repeat a page label or total count. Inspect renders only the final card page;
History is a separate Older/Newer surface backed by the immutable native Bria
archive on the origin node. Archive transcript bodies do not enter Raft state.

## Background status

Every live card repeats the three-per-row session switcher and appends a
bounded sticky panel for background sessions with `⏳`, `✅`, `❌`, or `❓`.
The panel is node-scoped in `host_first` and cluster-wide in `all_hosts`; only
the latter adds node markers and names. A completed status is not cleared by a
card repaint or leader failover. It is acknowledged by switching into that
specific session and disappears after the user's configured 1, 3, 5, or 10
acknowledging switches. Leaving a still-running dismissed session starts a new
working-status counter; terminal and action-required statuses stay dismissed
until the session emits a new event.

Completion, error, and action-required pushes are independent settings and
default to enabled. Delivery state is replicated after Telegram accepts the
push, so a new leader does not normally replay it. The unavoidable crash
window between Telegram acceptance and the Raft acknowledgement is
at-least-once: a duplicate is preferable to silently losing an alert. Working
state never sends a push. All action-required pushes target the sole owner.

## Cluster event log

Leadership changes and enabled-node reachability transitions use one compact
rolling Telegram message instead of one message per event. The body contains a
`Cluster` heading and at most the six newest timestamped rows: `👑 node` for a
new leader, `🔴 node` for loss, and `🟢 node` for recovery.

The existing message is edited only while it remains the newest chat message.
Any intervening owner message, Bria screen, session output, or document makes
the next cluster event start a new one-line log. This preserves chronological
context without editing session cards or making the owner scroll through a
burst of individual infrastructure notices. Initial startup state is seeded
silently; only subsequent transitions are reported.

## Single-owner boundary

Telegram projection and mutation are available only to the replicated sole
owner. Unknown users receive no object existence signal. Legacy share records
remain decodable for state compatibility, but the application service rejects
new sharing and owner transfer is a confirmation-bearing local operation.

## Card content privacy

The `Card content` category exposes three independent technical switches: tool
calls (names and arguments), tool results (output and diffs), and reasoning
blocks. All three default to visible for state compatibility with existing
profiles. Filtering happens before transcript events reach Telegram. A hidden
event disappears completely; it does not leave an empty spoiler or type
marker, while user prompts and narrative assistant text remain visible.

The same category has one response-card mode with three closed choices:

- keep every card and paginate the latest;
- keep every card, show only the latest page on the latest card, and omit its
  pagination row;
- delete the previous response card and paginate the latest.

The first mode is the default. The latest response-card identity is replicated
so a new Telegram leader can preserve replacement behavior after failover.

## Interactive prompt boundary

Every node detects supported Claude and Codex terminal prompts from its own
bounded tmux capture. Heartbeats replicate only the session generation, prompt
kind, and a truncated content hash; prompt text and the rest of the pane remain
on the origin node. The active response card switches to the keyboard
automatically. A background session gets a `❗` switcher marker and one compact
notification whose session button selects that node and session.

Interactive callback tokens bind actor, action, session, and prompt hash. The
origin executor captures the pane and verifies that hash again immediately
before sending an allowlisted tmux key. A stale prompt therefore cannot turn an
old Telegram click into input for a later CLI state. The keyboard is available
only to the sole owner.

The pane image is an unstructured pixel surface, so it has its own independent
working-only, always, or never setting. Tool-call, tool-result, and reasoning
visibility do not implicitly change the terminal snapshot policy.

## Semantic model and adapter contract

`Screen` owns text and a `Grid`; a `Grid` contains ordered `Row` values; a
`Button` owns a label and semantic `Callback`. These types contain no Telegram
SDK objects and are suitable for other adapters and deterministic tests.

Before sending a screen, an adapter must call `Screen.Validate`, encode every
callback, and register any carrier only after `AllowsDM` succeeds. Opaque
tokens should be actor-bound, short-lived or revision-bound. Stale, ambiguous
or cross-user tokens return the same not-found result and never reveal whether
the underlying private entity exists.

## Golden and flow regression policy

Golden tests compare `CanonicalGrid`, which records every row, label, semantic
action and opaque test token. Required cases are:

- exact 2/2/1 main Menu;
- host-first node picker with online/reconnecting/offline states;
- empty and populated node/session listings;
- all-host session counts of 1, 3, 4 and more than one full row;
- selected session marker and a node name on every all-host label;
- global and node-scoped archive lists, six-item pagination, and exclusion of
  `Lost` sessions;
- archive Inspect showing only the final page and separate Older/Newer History;
- mode-aware sticky background panels, three notification toggles, configurable
  1/3/5/10-switch acknowledgement, and replicated delivery state;
- busy and idle control cards;
- unknown-owner updates silently rejected before projection;
- callback encoding, invalid raw entity IDs and oversize rejection;
- private DM acceptance and group/channel/mismatched-chat rejection.

Transport and application flow tests cover private-DM rejection, leader-only
polling, durable cursor advancement, Menu rendering, opaque node selection,
stale/tampered token rejection and carrier editing. They also cover one
carrier through:

```text
live card -> Menu -> Sessions -> node -> session -> live card
live card -> Menu -> Sessions (all_hosts) -> remote session -> live card
Menu -> Archive -> Inspect -> Restore -> live card
Menu -> Settings -> Interface -> Session view -> All servers -> Back
card -> Settings -> Card content -> Response cards -> each of three modes
```

Every transition must verify the exact button grid, callback payload size,
actor authorization, DM scope, stale-token behavior and edit-in-place carrier
identity.
