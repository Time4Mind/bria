#!/bin/sh
# Idempotently start Bria's Android supervisor on an interactive root login.
# shellcheck disable=SC2317

if [ -w /sys/power/wake_lock ]; then
    echo bria > /sys/power/wake_lock 2>/dev/null
fi

if [ "$(id -u)" != "0" ]; then
    return 0 2>/dev/null || exit 0
fi
case "$-" in
    *i*) ;;
    *) return 0 2>/dev/null || exit 0 ;;
esac
if pgrep -f bria-supervisor.sh >/dev/null 2>&1; then
    return 0 2>/dev/null || exit 0
fi
command -v tmux >/dev/null 2>&1 || { return 0 2>/dev/null || exit 0; }

if ! tmux has-session -t bria 2>/dev/null; then
    tmux new-session -d -s bria -n __main__ -c /root
fi
if ! tmux list-windows -t bria -F '#{window_name}' 2>/dev/null | grep -qx __main__; then
    tmux new-window -t bria -n __main__ -c /root
fi
tmux send-keys -t bria:__main__ C-c 2>/dev/null
tmux send-keys -t bria:__main__ \
    "exec /root/bria/deploy/supervisor/bria-supervisor.sh" Enter 2>/dev/null
