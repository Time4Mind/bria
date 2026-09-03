#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: postflight.sh macos|linux|wsl ABSOLUTE_INSTALL_ROOT VERSION ABSOLUTE_CONFIG ABSOLUTE_SERVICE_FILE [--telegram]' >&2
	exit 2
}

test "$#" -eq 5 || test "$#" -eq 6 || usage
platform=$1
install_root=$2
version=$3
config=$4
service_file=$5
case "$platform" in macos|linux|wsl) ;; *) usage ;; esac
case "$install_root" in /*) ;; *) usage ;; esac
case "$config" in /*) ;; *) usage ;; esac
case "$service_file" in /*) ;; *) usage ;; esac
test -L "$install_root/current" || { printf '%s\n' 'postflight: current release is not active' >&2; exit 1; }
current_target=$(readlink "$install_root/current")
test "$current_target" = "releases/$version" || { printf '%s\n' 'postflight: active version mismatch' >&2; exit 1; }
bin_dir=$install_root/current
"$bin_dir/validate-install.sh" "$bin_dir" "$version" "$config" >/dev/null
"$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/service-control.sh" "$platform" status "$service_file"
if test "$#" -eq 6; then
	test "$6" = --telegram || usage
	"$bin_dir/bria" check-telegram --config "$config" >/dev/null
fi
printf '%s\n' "Bria $version postflight: OK"
