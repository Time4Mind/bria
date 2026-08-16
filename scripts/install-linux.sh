#!/bin/sh
set -eu

archive="${1:-}"
prefix="${BRIA_INSTALL_PREFIX:-/opt/bria}"
release="${BRIA_RELEASE_NAME:-$(date -u +%Y%m%d%H%M%S)}"
service_user="${BRIA_SERVICE_USER:-bria}"
data_dir="${BRIA_DATA_DIR:-/var/lib/bria}"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
[ -f "$archive" ] || { echo "usage: install-linux.sh RELEASE.tar.gz" >&2; exit 2; }
id "$service_user" >/dev/null 2>&1 || { echo "service user does not exist: $service_user" >&2; exit 1; }
case "$release" in ''|*[!A-Za-z0-9._-]*) echo "invalid release name" >&2; exit 2 ;; esac
case "$data_dir" in /*) ;; *) echo "BRIA_DATA_DIR must be absolute" >&2; exit 2 ;; esac

destination="$prefix/releases/$release"
[ ! -e "$destination" ] || { echo "release already exists: $destination" >&2; exit 1; }
mkdir -p "$prefix/releases"
stage="$(mktemp -d "$prefix/releases/.stage.XXXXXX")"
trap 'rm -rf "$stage"' EXIT INT TERM
tar -xzf "$archive" -C "$stage"
[ -x "$stage/bria" ] || { echo "archive has no executable bria" >&2; exit 1; }
"$stage/bria" version >/dev/null
chmod 0755 "$stage/bria"
# The control service owns releases, but an isolated runner has a distinct
# identity and must be able to traverse the immutable release directory.
chmod 0755 "$stage"
mv "$stage" "$destination"
ln -sfn "$destination" "$prefix/.current.new"
mv -Tf "$prefix/.current.new" "$prefix/current"

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  install -d -o root -g root -m 0755 "$data_dir/system-deps"
  install -d -o "$service_user" -g "$service_user" -m 0700 \
    "$data_dir/system-deps/requests"
  install -D -o root -g root -m 0755 \
    "$destination/deploy/systemd/bria-install-system-deps" \
    /usr/local/libexec/bria-install-system-deps
  install -D -o root -g root -m 0644 \
    "$destination/deploy/systemd/bria-system-deps.service" \
    /etc/systemd/system/bria-system-deps.service
  install -D -o root -g root -m 0644 \
    "$destination/deploy/systemd/bria-system-deps.path" \
    /etc/systemd/system/bria-system-deps.path
  install -d -o root -g root -m 0755 /etc/bria
  umask 077
  {
    printf 'BRIA_DATA_DIR=%s\n' "$data_dir"
    printf 'BRIA_SERVICE_USER=%s\n' "$service_user"
  } >/etc/bria/system-deps.env
  install -d -o root -g root -m 0755 /etc/systemd/system/bria-system-deps.path.d
  {
    printf '[Path]\n'
    printf 'DirectoryNotEmpty=\n'
    printf 'DirectoryNotEmpty=%s/system-deps/requests\n' "$data_dir"
  } >/etc/systemd/system/bria-system-deps.path.d/paths.conf
  systemctl daemon-reload
  systemctl enable --now bria-system-deps.path
fi
chown -R "$service_user" "$prefix/releases" "$prefix/current"
chown "$service_user" "$prefix"
trap - EXIT INT TERM
echo "installed $destination"
