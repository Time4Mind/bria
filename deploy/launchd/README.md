# macOS launchd deployment

Bria runs as the logged-in user so Claude/Codex credentials, project folders,
Apple Speech permission, and tmux belong to the same account. From a source
checkout, `scripts/install-macos.sh` builds and installs the Apple Silicon
binary under `~/.local/opt/bria/current`. If `~/.bria/config.json` already
exists it also loads the LaunchAgent; otherwise it prints the remaining
cluster init/join step and exits successfully.

The generated service PATH includes the user-local bin directory, Homebrew on
Apple Silicon and Intel, and the standard macOS system paths. Commands such as
`tmux`, `claude`, and `codex` are therefore resolved consistently under
launchd's otherwise minimal environment. Prefer absolute command paths in the
node config when a backend is installed elsewhere.

The installer writes a user LaunchAgent, validates it with `plutil`, and uses
`launchctl bootstrap`. It does not modify routes, DNS, firewall, proxy, system
daemons, or another user's files. It deliberately preserves the real `HOME`
so existing Claude/Codex credentials remain visible. Bria writes bounded log
buckets under `~/.bria/logs`: detailed timing files are retained for 6 hours,
service events for 24 hours, and critical failures for 72 hours. The LaunchAgent
still opens `node.log` as an early-start/runtime fallback; Bria adopts and bounds
that file on startup. To unload it:

```bash
launchctl bootout "gui/$(id -u)/com.time4mind.bria"
```
