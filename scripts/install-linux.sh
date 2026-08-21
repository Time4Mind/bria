#!/bin/sh
set -eu

archive="${1:-}"
prefix="${BRIA_INSTALL_PREFIX:-/opt/bria}"
requested_release="${BRIA_RELEASE_NAME:-}"
service_user="${BRIA_SERVICE_USER:-bria}"
data_dir="${BRIA_DATA_DIR:-/var/lib/bria}"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
[ -f "$archive" ] || { echo "usage: install-linux.sh RELEASE.tar.gz" >&2; exit 2; }
id "$service_user" >/dev/null 2>&1 || { echo "service user does not exist: $service_user" >&2; exit 1; }
case "$prefix" in /*) ;; *) echo "BRIA_INSTALL_PREFIX must be absolute" >&2; exit 2 ;; esac
case "$prefix$data_dir" in *"
"*) echo "install paths must not contain newlines" >&2; exit 2 ;; esac
case "$requested_release" in *[!A-Za-z0-9._-]*) echo "invalid release name" >&2; exit 2 ;; esac
case "$data_dir" in /*) ;; *) echo "BRIA_DATA_DIR must be absolute" >&2; exit 2 ;; esac

mkdir -p "$prefix/releases"
chown "$service_user" "$prefix" "$prefix/releases"
lock="$prefix/.install.lock"
if ! mkdir "$lock" 2>/dev/null; then
  owner=""
  [ ! -r "$lock/owner" ] || owner=$(sed -n '1p' "$lock/owner")
  case "$owner" in ''|*[!0-9]*) ;; *) kill -0 "$owner" 2>/dev/null && { echo "another Bria installation is active" >&2; exit 1; } ;; esac
  stale="$lock.stale.$$"
  mv -T "$lock" "$stale" 2>/dev/null || { echo "another Bria installation is active" >&2; exit 1; }
  rm -f "$stale/owner"
  rmdir "$stale"
  mkdir "$lock"
fi
printf '%s\n' "$$" >"$lock/owner"
stage="$(mktemp -d "$prefix/releases/.stage.XXXXXX")"
current_new="$prefix/.current.new.$$"
previous_new="$prefix/.previous.new.$$"
cleanup_linux_install() {
  [ -z "$stage" ] || rm -rf "$stage"
  rm -f "$current_new" "$previous_new"
  if [ -r "$lock/owner" ] && [ "$(sed -n '1p' "$lock/owner")" = "$$" ]; then
    rm -f "$lock/owner"
    rmdir "$lock" 2>/dev/null || true
  fi
}
trap cleanup_linux_install EXIT INT TERM
tar -xzf "$archive" -C "$stage"
[ -f "$stage/bria" ] && [ ! -L "$stage/bria" ] && [ -x "$stage/bria" ] || {
  echo "archive has no regular executable bria" >&2
  exit 1
}
if find "$stage" -type l -print -quit | grep . >/dev/null; then
  echo "release archive must not contain symlinks" >&2
  exit 1
fi
"$stage/bria" version >"$stage/release.json"
release_version=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' "$stage/release.json")
release_commit=$(sed -n 's/.*"commit":"\([^"]*\)".*/\1/p' "$stage/release.json")
release_built_at=$(sed -n 's/.*"built_at":"\([^"]*\)".*/\1/p' "$stage/release.json")
binary_sha256="$(sha256sum "$stage/bria" | awk '{print $1}')"
[ "${#binary_sha256}" -eq 64 ] || { echo "cannot calculate Bria binary identity" >&2; exit 1; }
grep -F '"binary_sha256":"'"$binary_sha256"'"' "$stage/release.json" >/dev/null || {
  echo "Bria version identity does not match the installed executable" >&2
  exit 1
}
if [ -n "$requested_release" ] && [ "$requested_release" != "$binary_sha256" ]; then
  echo "BRIA_RELEASE_NAME must equal the full Bria binary SHA-256" >&2
  exit 2
fi
release="$binary_sha256"
destination="$prefix/releases/$release"

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  install -d -o root -g root -m 0755 "$data_dir/system-deps"
  install -d -o "$service_user" -g "$service_user" -m 0700 \
    "$data_dir/system-deps/requests"
  install -D -o root -g root -m 0755 \
    "$stage/deploy/systemd/bria-install-system-deps" \
    /usr/local/libexec/bria-install-system-deps
  install -D -o root -g root -m 0644 \
    "$stage/deploy/systemd/bria-system-deps.service" \
    /etc/systemd/system/bria-system-deps.service
  install -D -o root -g root -m 0644 \
    "$stage/deploy/systemd/bria-system-deps.path" \
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
find "$stage" -mindepth 1 -maxdepth 1 ! -name bria ! -name release.json -exec rm -rf {} +
if [ -e "$destination" ]; then
  "$destination/bria" version >"$stage/existing-version.json"
  [ -d "$destination" ] && [ ! -L "$destination" ] &&
    cmp -s "$stage/bria" "$destination/bria" &&
    cmp -s "$stage/release.json" "$stage/existing-version.json" &&
    grep -F '"schema":1' "$destination/release.json" >/dev/null &&
    grep -F '"version":"'"$release_version"'"' "$destination/release.json" >/dev/null &&
    grep -F '"commit":"'"$release_commit"'"' "$destination/release.json" >/dev/null &&
    grep -F '"built_at":"'"$release_built_at"'"' "$destination/release.json" >/dev/null &&
    grep -F '"binary_sha256":"'"$binary_sha256"'"' "$destination/release.json" >/dev/null || {
      echo "existing immutable Bria release does not match its identity" >&2
      exit 1
    }
  rm -rf "$stage"
  stage=""
else
  chmod 0444 "$stage/release.json"
  chmod 0755 "$stage/bria"
  chmod 0755 "$stage"
  chown -R "$service_user" "$stage"
  chmod -R a-w "$stage"
  mv "$stage" "$destination"
  stage=""
fi
previous=""
prior_previous=""
if [ -L "$prefix/current" ]; then
  previous=$(readlink "$prefix/current")
elif [ -e "$prefix/current" ]; then
  echo "current activation path must be a symlink" >&2
  exit 1
fi
if [ -L "$prefix/previous" ]; then
  prior_previous=$(readlink "$prefix/previous")
elif [ -e "$prefix/previous" ]; then
  echo "previous activation path must be a symlink" >&2
  exit 1
fi
if [ -n "$previous" ]; then
  ln -s "$previous" "$previous_new"
  mv -Tf "$previous_new" "$prefix/previous"
fi
ln -s "$destination" "$current_new"
if ! mv -Tf "$current_new" "$prefix/current"; then
  if [ -n "$prior_previous" ]; then
    ln -s "$prior_previous" "$previous_new"
    mv -Tf "$previous_new" "$prefix/previous"
  else
    rm -f "$prefix/previous"
  fi
  exit 1
fi
trap - EXIT INT TERM
cleanup_linux_install
echo "installed $destination"
