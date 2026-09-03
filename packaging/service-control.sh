#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: service-control.sh macos|linux|wsl start|stop|restart|status ABSOLUTE_SERVICE_FILE' >&2
	exit 2
}

test "$#" -eq 3 || usage
platform=$1
action=$2
service_file=$3
case "$platform" in macos|linux|wsl) ;; *) usage ;; esac
case "$action" in start|stop|restart|status) ;; *) usage ;; esac
case "$service_file" in /*) ;; *) usage ;; esac
test -f "$service_file" || { printf '%s\n' 'service-control: rendered service file is unavailable' >&2; exit 1; }

if test "$platform" = macos; then
	label=com.time4mind.bria
	domain=gui/$(id -u)
	case "$action" in
		start)
			launchctl bootstrap "$domain" "$service_file"
			launchctl kickstart -k "$domain/$label"
			;;
		stop) launchctl bootout "$domain/$label" ;;
		restart) launchctl kickstart -k "$domain/$label" ;;
		status) launchctl print "$domain/$label" >/dev/null ;;
	esac
else
	unit=$(basename -- "$service_file")
	case "$unit" in *.service) ;; *) printf '%s\n' 'service-control: systemd unit must end in .service' >&2; exit 2 ;; esac
	case "$action" in
		start)
			systemctl --user link "$service_file" >/dev/null
			systemctl --user daemon-reload
			systemctl --user enable --now "$unit"
			;;
		stop) systemctl --user stop "$unit" ;;
		restart) systemctl --user restart "$unit" ;;
		status) systemctl --user is-active --quiet "$unit" ;;
	esac
fi
