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
service="gui/$(id -u)/com.time4mind.bria"
domain="gui/$(id -u)"
launchctl bootout "$service" 2>/dev/null || true

# bootout is asynchronous on macOS. Re-registering the same label before it
# disappears intermittently returns Bootstrap failed: 5 (EIO).
attempt=0
while launchctl print "$service" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 10 ] || {
    echo "timed out waiting for $service to stop" >&2
    exit 1
  }
  sleep 1
done

attempt=0
until launchctl bootstrap "$domain" "$destination"; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 5 ] || exit 1
  sleep 1
done

# A successful bootstrap only confirms LaunchAgent registration. Do not report
# installation success until the node serves a quorum-ready control response.
attempt=0
until "$binary" node probe --config "$config" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 20 ] || {
    echo "Bria was loaded but did not become ready after 20 probes" >&2
    exit 1
  }
  launchctl print "$service" >/dev/null 2>&1 || {
    echo "Bria exited before becoming ready" >&2
    exit 1
  }
  sleep 1
done
echo "installed $destination"
