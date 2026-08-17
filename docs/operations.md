# Operations

Bria changes one cluster member at a time. In automatic mode, an operator must
not restart enough voters concurrently to lose quorum. The commands below
modify only explicit paths and never change host routes, DNS, firewall, or
proxy settings.

## Leader policy

Open **Settings → Cluster → Leader selection**. Bria defaults to **Manual**:
choose one online, enabled node and confirm it. The assignment is replicated
and persistent. The selected node becomes the sole voter; other enabled nodes
remain nonvoters that can receive Raft log entries but cannot start elections.
The leader continues committing when all replicas disconnect. If it becomes
unavailable, the others wait. Choose **Automatic** only when ordinary Raft
quorum and leader failover should immediately move work to an elected node.

In manual mode an enabled nonleader that remains offline for two minutes is
automatically parked outside Raft replication. Parking is not `Disable`: the
server, sessions, settings and certificate identity remain registered. The
leader does not poll a parked host. When the host can again send an authenticated
heartbeat, Bria adds it back as a nonvoter and normal log catch-up resumes.
Failed heartbeat attempts on the returning host use a bounded 5–60 second
backoff, so reconnection can take up to one minute after network recovery.

On a new cluster with no assignment, the bootstrap leader is the provisional
sole voter and exposes a bounded setup screen so the owner can select a node or
enable automatic mode. The **Status** screen does not change leader policy.

To change a manual leader, keep the old leader and target online. Bria promotes
the target, transfers leadership through upstream Raft, then demotes the old
voter. If the old leader is already unavailable, transfer is deliberately
blocked until it returns; never force-bootstrap live Raft storage to work
around the outage. Automatic mode still follows ordinary quorum rules: one
member left from a three-voter configuration cannot commit or elect itself.

## Provider CLI setup

Open **Settings → Servers → _node_**. Claude Code and Codex are listed even
when absent. **Install** runs the official npm package under the provider
runtime user's `~/.bria/providers` tree, probes the resulting command, refreshes
the node inventory, and connects it to Bria. Isolated Linux nodes perform every
step inside the runner identity. The flow never invokes `sudo` or writes a
system npm prefix. The node or isolated runner must already provide a working
`npm`; if it does not, the setup screen reports that prerequisite instead of
changing the host package manager.

## Authenticated metrics

`bria node metrics --config PATH [--target NODE]` reads the Prometheus text
exposition from the existing node-control listener. The endpoint requires a
valid certificate for a current Raft member; there is no second diagnostics
port and no bearer token. Metrics are deliberately limited to process up,
readiness, leader presence, local Raft state, and applied index. Session names,
users, Telegram identifiers, provider accounts, quotas, transcripts, paths,
and network addresses are not exported.

## Renew a node certificate

Renewal generates the replacement private key on the node that will use it.
The CA host receives only a request containing the new public key, the current
certificate, and a short-lived signature made by the current node key.

On the target node, while its current certificate is valid:

```bash
bria node cert-request \
  --config /var/lib/bria/config.json \
  --request /secure-transfer/office-renewal.json \
  --state /var/lib/bria/office-renewal.private.json
```

The request can be transferred normally. The state file contains the new
private key, is created with owner-only permissions, and must stay on the
target node.

On the bootstrap CA host:

```bash
bria cluster cert-renew \
  --config /var/lib/bria/config.json \
  --request /secure-transfer/office-renewal.json \
  --confirm-node-id office \
  --response /secure-transfer/office-renewal-response.json
```

The issuer accepts only a request signed by a currently valid certificate for
the same cluster and node, plus an exact operator confirmation of that node ID.
This works for both statically configured and dynamically enrolled members
without treating possession of a CA-issued certificate as silent renewal
authorization. The request expires after 30 minutes. The default replacement
lifetime is 365 days and cannot exceed 397 days or the CA expiry.

Return the response to the target node and install it:

```bash
bria node cert-install \
  --config /var/lib/bria/config.json \
  --state /var/lib/bria/office-renewal.private.json \
  --response /secure-transfer/office-renewal-response.json
```

Installation verifies the CA chain, stable cluster/node identity, request ID,
and new key pair before making changes. It writes immutable credentials below
`DATA_DIR/pki/renewals/REQUEST_ID`, records the previous paths, and atomically
switches the config. The running process keeps its old in-memory credentials;
restart that one node, then run `bria node probe --config …` before continuing.
The private request state is removed only after a successful config switch.

If the restarted node cannot rejoin, restore the paths and restart it again:

```bash
bria node cert-rollback --config /var/lib/bria/config.json
```

Rollback first verifies that the previous files still form a valid certificate
for the configured identity. Old credential files are deliberately retained.
The replacement certificate carries a CA-signed link to the active key. Its
first heartbeat advances the replicated fingerprint, after which the old key
is rejected even on already-open Raft streams. Renewal is still not the right
response to suspected compromise because an attacker could race that
transition; disable the node and remove its membership instead.

## Logical cluster backup and restore

Create a backup from the current leader through the member-authenticated
control endpoint:

```bash
bria cluster backup \
  --config /var/lib/bria/config.json \
  --output /secure-backups/bria-2026-08-12.json
```

The command may start at any reachable member and follows its current-leader
hint using the leader's replicated control address and certificate fingerprint.
`--target NODE` remains available when the initial member must be chosen
explicitly. Brief leader transitions are retried within the bounded command
deadline.

The command creates a new owner-only file and refuses to overwrite an existing
path. It contains replicated product state and the idempotency ledger: node and
session metadata, preferences, archive metadata, navigation, quotas, and the
Telegram update cursor. It deliberately does not contain raw transcripts,
project files, native archive payloads, provider credentials, Telegram token,
callback key, node private keys, or the CA private key. Back up those local
artifacts separately according to their own retention policy.

The leader signs the envelope with its currently committed Ed25519 node key.
Restore checks the checksum, signature, CA chain, SPIFFE cluster/node identity,
and the signer's fingerprint in the backed-up state. A backup cannot be emitted
during the short certificate-rotation interval before the new fingerprint is
committed.

First preview without changing anything; this is allowed while the current
Raft directory exists:

```bash
bria cluster restore \
  --config /var/lib/bria/config.json \
  --input /secure-backups/bria-2026-08-12.json \
  --dry-run
```

Actual restore is intentionally a fresh-cluster recovery operation. Stop Bria,
move the existing `DATA_DIR/raft` directory to separately retained storage,
then stage the restore with an exact cluster-ID confirmation:

```bash
bria cluster restore \
  --config /var/lib/bria/config.json \
  --input /secure-backups/bria-2026-08-12.json \
  --confirm personal
```

Staging refuses a non-empty Raft directory and never deletes or overwrites it.
On the next bootstrap-node start, Bria applies the signed state in one
idempotent Raft command and renames the pending file to
`restore.applied.<digest>.json`. Bring other voters back one at a time only
after the restored leader is ready. Native session archives and transcripts
remain origin-local and must be restored separately if those files were lost.

## Rolling binary update

The normal path is **Settings → Cluster → Update cluster** in Telegram. Bria
fetches the official HTTPS manifest, verifies its pinned Ed25519 signature,
commits one update job to Raft, and updates nodes sequentially. Every node
downloads its own platform archive and verifies the manifest digest, archive
size, SHA-256, safe tar paths, and embedded `bria version`. Followers are
updated first. Bria waits for each heartbeat to report the target version,
transfers leadership to an updated healthy follower, and updates the former
leader last. Progress is one refreshable Telegram card, not one message per
node.

The activation symlink and previous target are retained locally. A detached
watchdog requires the new daemon to start its authenticated control endpoint
and Telegram adapter within 90 seconds; otherwise it restores the previous
target and terminates the failed process so its supervisor restarts the
rollback. The update job and per-node phases are replicated, so a newly elected
leader resumes the same rollout rather than starting another one.

Nodes installed before the updater endpoint existed require one normal manual
rolling installation of an updater-capable bridge release. That bootstrap is
unavoidable: an old process cannot accept the new authenticated update command.
After every enabled node runs the bridge release, subsequent compatible tagged
releases use the Telegram rolling-update flow above.

Official tagged releases are produced by `.github/workflows/release.yml`.
`BRIA_RELEASE_SIGNING_KEY` is a repository secret; only its public key is in
the node defaults and public repository. Never store the private release key
in a node config, release archive, log, or Git commit.

Use this manual sequence only as a recovery fallback when the signed updater
is unavailable:

1. Confirm every intended voter is reachable and ready with authenticated
   `bria node probe`; record the current leader and build versions.
2. Read the release compatibility note. A state-machine change must ship as a
   bridge release that reads both formats but writes only the old format until
   all voters run it. Bria rejects unknown command and snapshot versions rather
   than guessing.
3. Update and restart one follower. Wait for its probe to report a current
   leader and for the cluster status to show it online at the expected version.
4. Repeat for the remaining followers, never taking down a quorum. In a
   three-voter cluster, only one node may be unavailable at a time.
5. In automatic mode, transfer leadership to an already updated healthy voter,
   then update the old leader last. In manual mode, keep the selected leader as
   sole voter and restart it last; expect a short interaction outage while it
   returns and resumes the update coordinator.
6. Verify all probes, versions, provider status ages, session cards, and one
   harmless session round trip. Keep the previous binary and config available
   until this succeeds.

A manual sole-voter cluster necessarily has a short interaction outage while
its leader restarts, but nonvoting replicas do not block its commits. An
automatic two-voter cluster cannot commit while either voter is restarting;
Bria commits the next phase before stopping a node and continues only after
quorum returns. Do not mistake process health for quorum readiness,
force-bootstrap, or rewrite Raft storage during a normal update.

## External canary without Telegram UI automation

Before the visual Telegram acceptance pass, a deployment agent can safely run:

1. install the candidate binary and an independent Bria data directory;
2. run `bria version`, `bria doctor`, and an authenticated local probe;
3. enroll one remote node through an operator-provided invitation;
4. verify mTLS probes in both directions and record versions;
5. create a disposable provider session, restart only its origin node, and
   confirm recovery or archive follows the configured idle policy;
6. stop one follower, then the leader, separately, confirming quorum and
   leadership between steps;
7. remove the canary services without touching CCBot data or tmux namespaces.

Telegram rendering and button behavior remain an owner-authorized acceptance
step after the synthetic Bot API E2E pass.
Client API credentials are not a substitute for permission to drive the
owner's Telegram interface, and Android access is outside this runbook.

## Telegram polling diagnostics

Never call `getUpdates` manually with a token used by a running Bria node.
Telegram's update queue is consumptive: a diagnostic poll can remove an update
from Bria's polling stream or conflict with the elected leader. Diagnose stuck
updates through Bria logs, replicated cursor state, and synthetic Bot API tests.
Stop the cluster poller first only when an operator explicitly authorizes a
live queue inspection.
