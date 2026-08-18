#!/bin/sh
set -eu

project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
. "$project_root/scripts/macos-install-target.sh"
resolve_macos_install_target
data_dir="$resolved_data_dir"
config="$resolved_config"
binary="${BRIA_BINARY:-$HOME/.local/opt/bria/current/bria}"
template="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/com.time4mind.bria.plist"
destination="$HOME/Library/LaunchAgents/com.time4mind.bria.plist"

[ -r "$config" ] || { echo "config is not readable: $config" >&2; exit 1; }
[ -x "$binary" ] || { echo "bria binary is not executable: $binary" >&2; exit 1; }
case "$HOME$data_dir$config$binary" in *"
"*) echo "paths must not contain newlines" >&2; exit 1;; esac
mkdir -p "$HOME/Library/LaunchAgents" "$data_dir/logs"
xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' \
    -e 's/"/\&quot;/g' -e "s/'/\&apos;/g" -e 's/[|]/\\&/g'
}
escaped_home=$(xml_escape "$data_dir")
escaped_user_home=$(xml_escape "$HOME")
escaped_config=$(xml_escape "$config")
escaped_binary=$(xml_escape "$binary")
temporary="$destination.tmp.$$"
trap 'rm -f "$temporary"' EXIT HUP INT TERM
sed -e "s|__BRIA_HOME__|$escaped_home|g" \
	-e "s|__USER_HOME__|$escaped_user_home|g" \
  -e "s|__BRIA_CONFIG__|$escaped_config|g" \
  -e "s|__BRIA_BINARY__|$escaped_binary|g" \
  "$template" >"$temporary"
plutil -lint "$temporary" >/dev/null
chmod 0600 "$temporary"
mv "$temporary" "$destination"
trap - EXIT HUP INT TERM
"$binary" provider-hook --config "$config" --install
launchctl bootout "gui/$(id -u)/com.time4mind.bria" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$destination"
echo "installed $destination"
