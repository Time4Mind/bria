# Linux backend isolation

Bria treats provider agents on Linux as untrusted when `runner.mode` is
`docker`, `native-user`, or `wsl`. The node control process retains Raft keys,
the node mTLS private key, Telegram credentials, callback secrets, replicated
state, updates, and ACL decisions. Provider CLIs, tmux, provider credentials,
workspaces, naming calls, quota probes, and interactive provider login run
behind a local Unix-socket runner with none of those control-plane files.

The legacy default is `trusted`; this preserves Android, macOS, Windows, and
existing installations. A trusted node is intentionally not a future
multi-user security boundary.

## Invariants

- Never mount `/var/lib/bria`, `/etc/bria`, node PKI, the Telegram token,
  callback key, host root, or the Docker socket into the runner.
- The runner socket must be outside `data_dir`. It grants only the runner's
  existing OS privileges and must be accessible only to the runner and the
  Bria control user.
- Mount the runner home and workspaces at identical absolute paths in both
  environments. The control process needs read-only access for directory
  selection, transcripts, archives, and requested session files.
- Provider credentials belong only to the runner home. Do not place provider
  API keys in `bria-node.service`, `/etc/bria/proxy.env`, or node JSON.
- Do not grant the runner sudo, host administration groups, Docker access, or
  write access to the Bria binary/release directory.
- Isolation limits a compromised model to its Linux runner and workspace. It
  does not protect files deliberately mounted into that runner.

## Native Linux user

Create independent identities and a shared socket group:

```sh
sudo groupadd --system bria-runtime
sudo useradd --system --home-dir /var/lib/bria --shell /usr/sbin/nologin bria
sudo useradd --system --create-home --home-dir /srv/bria-agent --shell /usr/sbin/nologin bria-agent
sudo usermod -a -G bria-runtime bria
sudo usermod -a -G bria-runtime bria-agent
sudo install -d -o bria-agent -g bria-runtime -m 0750 /srv/bria-agent
sudo install -o root -g root -m 0644 deploy/systemd/bria-runner.service /etc/systemd/system/
sudo install -d -o root -g root -m 0755 /etc/systemd/system/bria-node.service.d
sudo install -o root -g root -m 0644 deploy/systemd/bria-node-isolated.conf \
  /etc/systemd/system/bria-node.service.d/runner.conf
```

Set this in the node configuration:

```json
"runner": {
  "mode": "native-user",
  "socket": "/run/bria-runner/runner.sock",
  "home": "/srv/bria-agent"
}
```

Then run `bria node isolation-check --config /etc/bria/node.json` before
starting the node. The check rejects root runners, the same UID as control,
containers mislabeled as native users, and an unavailable socket.

## Docker

`deploy/docker/compose.runner.yml` builds only the runner. Extend its image to
install the required provider CLIs, or bind-mount immutable executables. Share
`BRIA_RUNNER_SOCKET_DIR` with the host control process at
`/run/bria-runner`; create it as `0770` with the runner UID and the numeric
`BRIA_RUNTIME_GID`, then add the control user to that group. Share
`BRIA_RUNNER_HOME` read-only with control and
read-write with the runner. Use `mode: docker` and `home: /srv/bria-agent` if
that is the identical visible path. The control preflight requires a distinct
mount namespace and a non-root container process.

## WSL

For a security boundary, use a dedicated WSL distribution for the runner and
disable both Windows executable interop and Windows drive automounts there.
Bria rejects `mode: wsl` while `/proc/sys/fs/binfmt_misc/WSLInterop` exists or
Windows drives are visible. A second Linux user in an ordinary WSL distro is
not sufficient: it can otherwise execute Windows programs and reach files as
the Windows user. If the control and runner distributions cannot safely share
a Unix socket and identical workspace paths, use Docker isolation instead.

## Security scope

The runner protocol contains no generic control-plane operation: it can run
commands only with the runner's own UID and can service provider login inside
that same account. It cannot issue Raft commands, change node access, enroll a
node, read another node's transcript, or authenticate to node control because
the corresponding keys never cross the boundary. Multi-user mode remains
disabled; if added later, startup must require a passing isolated-runner
preflight on every Linux execution node.
