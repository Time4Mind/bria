#!/usr/bin/env bash
# Keep the Bria node and its outbound-only SSH transport alive on hosts
# without systemd (notably the Android chroot used for CCBot).

set -u

state_dir="${BRIA_DATA_DIR:-$HOME/.bria}"
binary="${BRIA_BINARY:-$HOME/.local/bin/bria}"
config="${BRIA_CONFIG:-$state_dir/config.json}"
ssh_target="${BRIA_SSH_TARGET:-}"
restart_floor="${BRIA_RESTART_BACKOFF:-5}"
backoff_max="${BRIA_BACKOFF_MAX:-120}"
healthy_run_sec="${BRIA_HEALTHY_RUN_SEC:-120}"

# Non-login shells spawned by tmux commonly omit the per-user binary
# directory. Bria itself may still be reachable through an absolute symlink,
# while backend discovery incorrectly reports that every provider is absent.
case ":${PATH:-}:" in
    *":$HOME/.local/bin:"*) ;;
    *) export PATH="$HOME/.local/bin:${PATH:-/usr/local/bin:/usr/bin:/bin}" ;;
esac

peer_one="${BRIA_TUNNEL_PEER_ONE:-}"
peer_two="${BRIA_TUNNEL_PEER_TWO:-}"
peer_three="${BRIA_TUNNEL_PEER_THREE:-}"
peer_one_control="${BRIA_TUNNEL_PEER_ONE_CONTROL:-}"
peer_two_control="${BRIA_TUNNEL_PEER_TWO_CONTROL:-}"
peer_three_control="${BRIA_TUNNEL_PEER_THREE_CONTROL:-}"
reverse_raft="${BRIA_TUNNEL_REVERSE_RAFT:-127.0.0.1:17946:127.0.0.1:7946}"
reverse_control="${BRIA_TUNNEL_REVERSE_CONTROL:-127.0.0.1:17947:127.0.0.1:7947}"
reverse_enrollment="${BRIA_TUNNEL_REVERSE_ENROLLMENT:-127.0.0.1:17948:127.0.0.1:7948}"

mkdir -p "$state_dir/logs"
chmod 700 "$state_dir" "$state_dir/logs"

log() {
    printf '[%s] bria-supervisor: %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

for number in "$restart_floor" "$backoff_max" "$healthy_run_sec"; do
    case "$number" in
        ''|*[!0-9]*) log "invalid numeric supervisor setting"; exit 2 ;;
    esac
done

if [ ! -x "$binary" ] || [ ! -r "$config" ]; then
    log "binary or config unavailable (binary=$binary config=$config)"
    exit 1
fi
[ -n "$ssh_target" ] || {
    log "BRIA_SSH_TARGET is required"
    exit 1
}
for command in ssh flock nc; do
    command -v "$command" >/dev/null 2>&1 || {
        log "required command is unavailable: $command"
        exit 1
    }
done

exec 9>"$state_dir/supervisor.lock"
if ! flock -n 9; then
    log "another supervisor owns $state_dir/supervisor.lock; exiting"
    exit 0
fi

tunnel_pid=""
node_pid=""
cleanup_children() {
    for pid in "$node_pid" "$tunnel_pid"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
        fi
    done
    for pid in "$node_pid" "$tunnel_pid"; do
        [ -n "$pid" ] && wait "$pid" 2>/dev/null || true
    done
}
shutdown() {
    trap - INT TERM EXIT
    cleanup_children
    exit 0
}
trap shutdown INT TERM EXIT

ssh_options=(
    -N
    -o BatchMode=yes
    -o ConnectTimeout=10
    -o ExitOnForwardFailure=yes
    -o ServerAliveInterval=15
    -o ServerAliveCountMax=3
    -o TCPKeepAlive=yes
    -R "$reverse_raft"
    -R "$reverse_control"
    -R "$reverse_enrollment"
)
local_forwards=()
for forward in "$peer_one" "$peer_two" "$peer_three" \
    "$peer_one_control" "$peer_two_control" "$peer_three_control"; do
    if [ -n "$forward" ]; then
        ssh_options+=( -L "$forward" )
        local_forwards+=( "$forward" )
    fi
done
probe_options=(
    -o BatchMode=yes
    -o ConnectTimeout=8
    -o ServerAliveInterval=5
    -o ServerAliveCountMax=1
)

local_forward_ready() {
    for forward in "${local_forwards[@]}"; do
        address="${forward%%:*}"
        remainder="${forward#*:}"
        port="${remainder%%:*}"
        nc -z -w 2 "$address" "$port" >/dev/null 2>&1 || return 1
    done
}

backoff="$restart_floor"
log "starting (node=$binary tunnel=$ssh_target)"
while true; do
    until ssh "${probe_options[@]}" "$ssh_target" true >/dev/null 2>&1; do
        log "SSH transport unavailable; retrying in ${backoff}s"
        sleep "$backoff"
        backoff=$((backoff * 2))
        [ "$backoff" -gt "$backoff_max" ] && backoff="$backoff_max"
    done

    # The target is intentionally selected locally; no remote expansion is used.
    # shellcheck disable=SC2029
    ssh "${ssh_options[@]}" "$ssh_target" >>"$state_dir/logs/tunnel.log" 2>&1 &
    tunnel_pid=$!
    ready=0
    for _ in $(seq 1 20); do
        if ! kill -0 "$tunnel_pid" 2>/dev/null; then
            break
        fi
        if local_forward_ready; then
            ready=1
            break
        fi
        sleep 1
    done
    if [ "$ready" -ne 1 ]; then
        log "SSH forwards did not become ready"
        cleanup_children
        tunnel_pid=""
        node_pid=""
        sleep "$backoff"
        continue
    fi

    started_at="$(date +%s)"
    "$binary" node run --config "$config" >>"$state_dir/logs/node.log" 2>&1 &
    node_pid=$!
    log "node and SSH transport running"

    while kill -0 "$tunnel_pid" 2>/dev/null && kill -0 "$node_pid" 2>/dev/null; do
        sleep 2
    done
    ran_for=$(( $(date +%s) - started_at ))
    log "node or transport exited after ${ran_for}s; restarting in ${backoff}s"
    cleanup_children
    tunnel_pid=""
    node_pid=""
    if [ "$ran_for" -ge "$healthy_run_sec" ]; then
        backoff="$restart_floor"
    else
        backoff=$((backoff * 2))
        [ "$backoff" -gt "$backoff_max" ] && backoff="$backoff_max"
    fi
    sleep "$backoff"
done
