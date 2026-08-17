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
command -v flock >/dev/null 2>&1 || {
    log "required command is unavailable: flock"
    exit 1
}
if [ -n "$ssh_target" ]; then
    for command in ssh nc; do
        command -v "$command" >/dev/null 2>&1 || {
            log "required transport command is unavailable: $command"
            exit 1
        }
    done
fi

exec 9>"$state_dir/supervisor.lock"
if ! flock -n 9; then
    log "another supervisor owns $state_dir/supervisor.lock; exiting"
    exit 0
fi

tunnel_pid=""
node_pid=""
stop_child() {
    pid="$1"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
    fi
    [ -n "$pid" ] && wait "$pid" 2>/dev/null || true
}
cleanup_children() {
    stop_child "$node_pid"
    stop_child "$tunnel_pid"
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

start_node() {
    node_started_at="$(date +%s)"
    "$binary" node run --config "$config" >>"$state_dir/logs/node.log" 2>&1 &
    node_pid=$!
    log "node running"
}

start_tunnel() {
    # The target is intentionally selected locally; no remote expansion is used.
    # shellcheck disable=SC2029
    ssh "${ssh_options[@]}" "$ssh_target" >>"$state_dir/logs/tunnel.log" 2>&1 &
    tunnel_pid=$!
    tunnel_started_at="$(date +%s)"
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
    if [ "$ready" -eq 1 ]; then
        log "SSH transport running"
        return 0
    fi
    log "SSH forwards did not become ready"
    stop_child "$tunnel_pid"
    tunnel_pid=""
    return 1
}

node_backoff="$restart_floor"
tunnel_backoff="$restart_floor"
next_tunnel_attempt=0
log "starting (node=$binary tunnel=${ssh_target:-disabled})"
start_node
while true; do
    if ! kill -0 "$node_pid" 2>/dev/null; then
        wait "$node_pid" 2>/dev/null || true
        ran_for=$(( $(date +%s) - node_started_at ))
        log "node exited after ${ran_for}s; restarting in ${node_backoff}s"
        node_pid=""
        if [ "$ran_for" -ge "$healthy_run_sec" ]; then
            node_backoff="$restart_floor"
        else
            node_backoff=$((node_backoff * 2))
            [ "$node_backoff" -gt "$backoff_max" ] && node_backoff="$backoff_max"
        fi
        sleep "$node_backoff"
        start_node
        continue
    fi

    if [ -z "$ssh_target" ]; then
        sleep 2
        continue
    fi

    now="$(date +%s)"
    if [ -n "$tunnel_pid" ] && ! kill -0 "$tunnel_pid" 2>/dev/null; then
        wait "$tunnel_pid" 2>/dev/null || true
        ran_for=$(( now - tunnel_started_at ))
        log "SSH transport exited after ${ran_for}s; retrying in ${tunnel_backoff}s"
        tunnel_pid=""
        if [ "$ran_for" -ge "$healthy_run_sec" ]; then
            tunnel_backoff="$restart_floor"
        else
            tunnel_backoff=$((tunnel_backoff * 2))
            [ "$tunnel_backoff" -gt "$backoff_max" ] && tunnel_backoff="$backoff_max"
        fi
        next_tunnel_attempt=$((now + tunnel_backoff))
    fi

    if [ -z "$tunnel_pid" ] && [ "$now" -ge "$next_tunnel_attempt" ]; then
        if ssh "${probe_options[@]}" "$ssh_target" true >/dev/null 2>&1 && start_tunnel; then
            tunnel_backoff="$restart_floor"
            next_tunnel_attempt=0
        else
            log "SSH transport unavailable; node remains online; retrying in ${tunnel_backoff}s"
            next_tunnel_attempt=$((now + tunnel_backoff))
            tunnel_backoff=$((tunnel_backoff * 2))
            [ "$tunnel_backoff" -gt "$backoff_max" ] && tunnel_backoff="$backoff_max"
        fi
    fi

    sleep 2
done
