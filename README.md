# Bria

Bria is a fault-tolerant, multi-node control plane for Codex and Claude Code
terminal sessions. Telegram is the first interaction adapter, not part of the
cluster core. Bria is a separate project from CCBot: both can
run on the same host with independent processes, tmux namespaces, state, hooks,
and Telegram bots.

The production implementation is Go. The repository currently also retains
the earlier Python prototype as an executable behavioral reference while its
tested runtime and protocol invariants are ported; it is not the production
cluster foundation.

## Product invariants

- leader selection is manual by default; the selected leader is the sole Raft
  voter, while other enabled nodes are replication-only members;
- the owner assigns the leader under `Settings → Cluster`; automatic selection
  is an explicit mode that promotes enabled nodes to ordinary Raft voters;
- only the selected node, while it is also the current Raft leader, runs
  interactive adapters and cluster work; other nodes wait for it;
- global metadata is replicated through Raft; raw provider transcripts remain
  on their origin node;
- sessions use node-qualified identity and remain attached to their origin;
- the current product mode has exactly one Telegram owner; sessions are
  private, every enrolled node is visible to that owner, and sharing is
  disabled until a separate multi-user security model is approved;
- Telegram is private-DM only;
- `host_first` is the default session view; `all_hosts` is optional;
- Telegram screens preserve CCBot's card, menu, button, and pagination flow;
- a daemon restart reattaches tmux; after an OS reboot, sessions whose idle
  deadline has not passed are resumed on their origin node, while expired or
  non-resumable sessions are archived;
- native archives do not depend on CCBot; they retain a bounded, portable Bria
  event artifact and use provider resume metadata only for seamless in-place
  restoration when it is still available on the origin node.

See [Go architecture](docs/go-architecture.md),
[Linux backend isolation](docs/linux-runner-isolation.md),
[Telegram parity](docs/telegram-parity.md), and
[product decisions](docs/product-decisions.md). Certificate renewal, rolling
updates, rollback, and canary checks are documented in
[operations](docs/operations.md). Contributions must also follow
the pragmatic Go engineering contract in [CONTRIBUTING.md](CONTRIBUTING.md).

## Implemented Go foundation

- actor-first domain model with a replicated sole-owner boundary and legacy
  grant decoding kept only for snapshot compatibility;
- deterministic versioned command state machine with bounded idempotency;
- HashiCorp Raft, Bolt log/stable store, snapshots, membership and leadership
  transfer primitives;
- TLS 1.3 mutual authentication with CA validation and stable SPIFFE-style
  node identity, independent of IP address;
- Telegram-approved enrollment with one-use 30-minute invitations,
  node-generated keys, pinned TLS certificate delivery, signed-contract/manual-claim
  fallback, 24-hour pending requests, and dynamic Raft suffrage reconciliation;
- explicit Active → Disabled → Deleted lifecycle, restricted maintenance mode,
  close-and-archive error reporting, identity tombstones, and renameable node
  names with automatic duplicate suffixes;
- atomic node-local native archive artifacts, SHA-256 integrity verification,
  restore-on-origin with queued session actions, idle/retention policies, and
  deterministic purge planning;
- Linux and Darwin boot identity providers;
- Claude, Codex, and tmux capability probes plus idempotent reboot recovery
  without shell interpolation; externally removed tmux windows are detected
  during normal operation and become native archives rather than `Lost`;
- optional Docker, native-user, and hardened WSL runner isolation for
  untrusted Linux provider agents, keeping node keys, Telegram secrets, Raft,
  updates, and access decisions in a separate control identity, with an
  owner/admin-enforced policy selected independently for each node;
- node-settings installation of missing Claude Code or Codex CLIs into a
  user-owned node/runner prefix, followed by a version probe and explicit Bria
  connection; host discovery alone never grants access to a provider;
- a transport-neutral interaction lifecycle plus CCBot-compatible Telegram
  grids, actor-bound opaque callbacks, a bounded Bot API client, private-DM
  parser, and leader-gated durable polling;
- ordered text, voice, photo, and document intake pinned to the selected
  session, with direct target-node Telegram downloads, node-local whisper.cpp
  or macOS Apple Speech transcription, and archived `.bria-inbox` attachments;
- bounded node-local Claude/Codex transcript readers, leader-to-origin mTLS
  retrieval, CCBot-style live-card pagination, expandable technical blocks,
  a text-only rich-message fallback, and restart reconciliation of missed
  provider completions without replicating transcript text;
- automatic two-word session naming on the origin node after `Clear`, using
  configurable lightweight models (`haiku` and `gpt-5.6-luna` by default);
- host-first/all-hosts/archive projectors with edit-in-place routing for the
  implemented Menu and selection flows;
- CCBot-style hierarchical settings with replicated per-user English,
  Russian, and Chinese localization, including independent tool-call,
  tool-result, and reasoning visibility plus three response-card retention
  modes;
- sticky node-aware background status panels, transition-only completion,
  error, and action-required notifications, and configurable replicated
  dismissal after 1, 3, 5, or 10 switches into the session;
- a cluster-wide Rich Markdown status table with per-node/provider quota
  snapshots, data age, offline cache, async refresh, leader marking, and global
  node sorting; persistent manual/automatic leader policy lives in cluster
  settings rather than Status;
- node-local Claude and Codex quota collectors with replicated normalized
  snapshots, five/ten-minute polling, daily budget tracking, unique-account
  threshold alerts, and no provider credentials or raw terminal output in
  Raft;
- sole-voter continuity plus three-voter Raft replication and automatic
  leader-failover integration coverage;
- follower-safe, node-local Telegram warnings when a running node loses and
  later restores cluster connectivity;
- authenticated health/readiness probes that distinguish a live process from
  a node with a current Raft leader, an mTLS-only Prometheus metrics endpoint,
  plus process-level disk/mTLS failover chaos coverage;
- owner-initiated signed rolling updates with per-node HTTPS downloads,
  replicas-first ordering, automatic-mode leadership transfer, manual-mode
  sole-leader restart, replicated progress, and a detached rollback watchdog;
- bootstrap CLI and a persistent single-node daemon vertical slice.

## Build and verify

The current pinned toolchain is Go 1.26.6.

```bash
go test ./...
go vet ./...
go build -trimpath ./cmd/bria
```

CI additionally runs the race detector and cross-builds Linux ARM64, macOS
Apple Silicon, and the future Windows target. `scripts/build-release.sh`
produces reproducible-style, checksummed archives for Linux AMD64/ARM64,
macOS Apple Silicon, and Windows AMD64. Linux systemd and macOS user-launchd
installation assets are included; native Windows service supervision remains
outside the current support promise.

On an Apple Silicon Mac, a deployment agent (including one invoked through
CCBot) can clone this repository and run `scripts/install-macos.sh`. The
idempotent user-level installer builds Bria and the Apple Speech helper,
installs them under `~/.local/opt/bria/current`, and loads launchd once the
node configuration exists. It does not change system networking, proxy, DNS,
firewall, or another user's files; it never copies provider or Telegram
credentials into Git.

```bash
gh repo clone Time4Mind/bria "$HOME/.local/src/bria"
"$HOME/.local/src/bria/scripts/install-macos.sh"
```

## Bootstrap smoke flow

```bash
bria cluster init \
  --cluster-id personal \
  --node-id phone \
  --node-name Phone \
  --owner-user-id 123456789 \
  --raft-bind 0.0.0.0:7946 \
  --raft-advertise phone.internal:7946

bria node run --config ~/.bria/config.json
```

An authenticated local or peer probe returns the node ID, Raft state, current
leader, and applied index. The default fails when no current leader is known;
`--health-only` checks only that the target process and control server respond:

```bash
bria node probe --config ~/.bria/config.json
bria node probe --config ~/.bria/config.json --target office
bria node probe --config ~/.bria/config.json --health-only
```

The command uses the configured node certificate and never exposes an
unauthenticated diagnostics port. A non-ready response is emitted as JSON and
also returns a non-zero exit status for supervisors.

Prometheus-format process, readiness, Raft-state, and applied-index metrics use
the same mTLS control endpoint and reveal no sessions, users, Telegram data, or
provider data. A cluster member can retrieve them for a local or configured
peer without placing credentials in a URL:

```bash
bria node metrics --config ~/.bria/config.json
bria node metrics --config ~/.bria/config.json --target office
```

Replicated state can be exported from the current leader and inspected before
a fresh-Raft disaster recovery:

```bash
bria cluster backup --config ~/.bria/config.json --output /secure/bria.json
bria cluster restore --config ~/.bria/config.json --input /secure/bria.json --dry-run
```

The backup command may be run against any reachable member; it discovers and
cryptographically verifies the current leader before writing the file.

See [operations](docs/operations.md) for the signed-backup contents, exact
restore confirmation, and the required preservation of existing Raft and
node-local archive data.

`cluster init` refuses to overwrite an existing identity and writes keys,
certificates, configuration, and Raft storage with owner-only permissions.
The numeric owner ID is replicated with access to the bootstrap node. Telegram
remains disabled while `telegram_token_file` is absent or empty; the token is
operator-provided and is never written by `cluster init`.

The sole owner can be changed only from the bootstrap host with an exact
numeric confirmation. The desired identity is read when that node starts and
is committed when it is cluster leader; control of existing sessions and the
preferences move to the new private owner, historical archive provenance stays
immutable, and the old Telegram identity loses access:

```bash
bria cluster set-owner --config ~/.bria/config.json \
  --user-id 987654321 --confirm 987654321
```

Every voting node that may become Telegram leader or host a media-consuming
session needs the same token in its local `telegram_token_file`; it is never
replicated through Raft. Voice uses `speech_engine: "whisper"` by default,
with `ffmpeg`, `whisper-cli`, and the node-local `whisper_model_path`. On a
systemd Linux/WSL installation, enabling Whisper automatically requests the
fixed `speech` dependency profile; enabling Claude or Codex similarly requests
Node.js/npm when absent. The root-owned helper accepts no arbitrary packages
or commands from the node process.

On macOS Apple Silicon, `speech_engine: "apple"` selects Apple's built-in,
strictly on-device recognizer and does not require a Whisper model. Build the
narrow helper with `scripts/build-apple-speech.sh`, set its path in
`apple_speech_command`, and run `bria-apple-speech --authorize` once from that
macOS user session. `whisper_language` is reused as the locale (`auto` means
the current macOS locale). `ffmpeg` remains responsible only for converting
Telegram OGG/Opus audio to a system-readable WAV file.

## Add a node

The primary flow is `Settings → Cluster → Connect node → Invitation`. Give the
displayed value to the deployment agent and run:

```bash
bria cluster join \
  --invite 'bria1.…' \
  --node-name Office \
  --raft-bind 0.0.0.0:7946 \
  --raft-advertise office.internal:7946
```

The command creates its private key locally, submits a signed request, waits
for the separate Telegram approval card, and writes configuration only after
approval. The invitation is single-use and expires after 30 minutes; the
consumed pending request remains for 24 hours.

For deployments that must return their connection material first, use
`bria cluster contract … --state /secure/path/node-contract.json`, paste its
one-line output through `Connect node → Node contract`, approve it, and return
the displayed claim to the agent with
`bria cluster claim --claim 'bria-claim1.…' --state /secure/path/node-contract.json`.
The owner-only staging file is removed after a successful claim.

### Nodes reached through local tunnels

Stable cluster addresses and machine-local tunnel endpoints are deliberately
separate. In a node's private `config.json`, a peer may define
`dial_address` and `control_dial_address` alongside its replicated `address`
and `control_address`:

```json
{
  "node_id": "peer-a",
  "node_name": "Peer A",
  "address": "peer-a.bria.internal:7946",
  "control_address": "peer-a.bria.internal:7947",
  "dial_address": "127.0.0.1:19046",
  "control_dial_address": "127.0.0.1:19047"
}
```

Only the stable addresses enter Raft and replicated node metadata. Dial
overrides stay in that one host's config, survive peer relocation by node ID,
and do not weaken certificate identity, fingerprint, lifecycle, or tombstone
checks. Omit either override when the stable address is directly reachable.

For reverse enrollment, bring up the SSH forward before requesting approval
and pass its local endpoint with `--enrollment-dial-address`. The invitation or
claim retains the stable issuer endpoint for identity while the enrollment
client connects through the loopback tunnel. A reverse-connected node should
use loopback `--raft-bind`/`--control-bind` and stable relay-facing
`--raft-advertise`/`--control-advertise` values from the start; never advertise
a temporary Wi-Fi address. The Android supervisor also supports
`BRIA_TUNNEL_REVERSE_ENROLLMENT` and forwards the conventional enrollment port
in addition to Raft and node control.

Internet proxying is also node-local. Set exactly one of `http_proxy` or
`socks5_proxy` in the private node config. It applies to Telegram Bot API and
signed release downloads; Raft and node-control transports remain direct mTLS
connections. If both settings are omitted, Bria connects directly, so a node
whose network resolves and reaches Telegram works without a VPN. A configured
proxy is strict: Bria does not silently bypass it when that proxy is
unavailable.

## Status

Bria is feature-complete for the current single-owner scope and passes unit,
race, cross-build, process-chaos, recovery, and architecture checks. A real
three-host deployment and owner-visible Telegram acceptance remain explicit
operator gates before treating a particular build as approximately production.
Routine certificate renewal and deleted/rotated identity revocation are
implemented; suspected key compromise still requires disabling and deleting
the affected node rather than relying on renewal.
