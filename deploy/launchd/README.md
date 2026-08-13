# macOS launchd deployment

Bria runs as the logged-in user so Claude/Codex credentials, project folders,
Apple Speech permission, and tmux belong to the same account. From a source
checkout, `scripts/install-macos.sh` builds and installs the Apple Silicon
binary under `~/.local/opt/bria/current`. If `~/.bria/config.json` already
exists it also loads the LaunchAgent; otherwise it prints the remaining
cluster init/join step and exits successfully.

The installer writes a user LaunchAgent, validates it with `plutil`, and uses
`launchctl bootstrap`. It does not modify routes, DNS, firewall, proxy, system
daemons, or another user's files. It deliberately preserves the real `HOME`
so existing Claude/Codex credentials remain visible. Logs go to
`~/.bria/logs/node.log` by
default. To unload it:

```bash
launchctl bootout "gui/$(id -u)/com.time4mind.bria"
```
