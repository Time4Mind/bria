#!/bin/sh
set -eu

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "Bria currently supports this installer only on macOS Apple Silicon" >&2
  exit 1
fi
if [ "$(id -u)" -eq 0 ]; then
  echo "run as the macOS login user, not root" >&2
  exit 1
fi

project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
. "$project_root/scripts/macos-release-layout.sh"
. "$project_root/scripts/build-provenance.sh"
if [ "${BRIA_INSTALL_PREFIX+x}" = x ] && [ "${BRIA_INSTALL_ROOT+x}" = x ]; then
  echo "set either BRIA_INSTALL_PREFIX or legacy BRIA_INSTALL_ROOT, not both" >&2
  exit 2
fi
if [ "${BRIA_INSTALL_ROOT+x}" = x ]; then
  current="$BRIA_INSTALL_ROOT"
  install_prefix=$(dirname -- "$current")
else
  install_prefix="${BRIA_INSTALL_PREFIX:-$HOME/.local/opt/bria}"
  current="$install_prefix/current"
fi
case "$install_prefix" in /*) ;; *) echo "Bria install prefix must be absolute" >&2; exit 2 ;; esac
case "$current" in /*) ;; *) echo "Bria activation path must be absolute" >&2; exit 2 ;; esac
case "$install_prefix$current" in *"
"*) echo "Bria install paths must not contain newlines" >&2; exit 2 ;; esac
releases="$install_prefix/releases"
. "$project_root/scripts/macos-install-target.sh"
resolve_macos_install_target
data_dir="$resolved_data_dir"
config="$resolved_config"
go_binary="${GO_BINARY:-$(command -v go || true)}"

[ -n "$go_binary" ] && [ -x "$go_binary" ] || {
  echo "Go is required to build Bria; install the pinned version from go.dev and retry" >&2
  exit 1
}
require_clean_tracked_tree "$project_root"
source_head=$(git -C "$project_root" rev-parse HEAD)

staging=$(mktemp -d "${TMPDIR:-/tmp}/bria-install.XXXXXX")
release_stage=""
current_new=""
previous_new=""
install_lock=""
cleanup_install() {
  rm -rf "$staging"
  [ -z "$release_stage" ] || rm -rf "$release_stage"
  [ -z "$current_new" ] || rm -f "$current_new"
  [ -z "$previous_new" ] || rm -f "$previous_new"
  [ -z "$install_lock" ] || macos_release_install_lock "$install_lock"
}
trap cleanup_install EXIT HUP INT TERM
version=$(git -C "$project_root" describe --tags --always --dirty 2>/dev/null || printf development)
commit=$(git -C "$project_root" rev-parse HEAD 2>/dev/null || printf unknown)
built_at=$(date +%s)
validate_build_version "$version"
validate_build_commit "$commit"
validate_build_timestamp "$built_at"
link_flags="-s -w -X github.com/Time4Mind/bria/internal/buildinfo.Version=$version -X github.com/Time4Mind/bria/internal/buildinfo.Commit=$commit -X github.com/Time4Mind/bria/internal/buildinfo.BuiltAt=$built_at"

(cd "$project_root" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$go_binary" build -trimpath \
  -ldflags "$link_flags" -o "$staging/bria" ./cmd/bria)
"$project_root/scripts/build-apple-speech.sh" "$staging/bria-apple-speech"
mkdir -p "$staging/installer/deploy/launchd" "$staging/installer/scripts"
cp "$project_root/deploy/launchd/install-user.sh" \
  "$project_root/deploy/launchd/com.time4mind.bria.plist" \
  "$staging/installer/deploy/launchd/"
cp "$project_root/scripts/macos-install-target.sh" \
  "$project_root/scripts/launch-current-macos.sh" "$staging/installer/scripts/"
verify_build_snapshot "$project_root" "$source_head"
install_user="$staging/installer/deploy/launchd/install-user.sh"
binary_sha256=$(/usr/bin/shasum -a 256 "$staging/bria" | awk '{print $1}')
[ "${#binary_sha256}" -eq 64 ] || { echo "cannot calculate Bria binary identity" >&2; exit 1; }
"$staging/bria" version >"$staging/release.json"
grep -F '"binary_sha256":"'"$binary_sha256"'"' "$staging/release.json" >/dev/null || {
  echo "Bria version identity does not match the built executable" >&2
  exit 1
}
release="$releases/$binary_sha256"
mkdir -p "$releases" "$data_dir"
install_lock_path="$install_prefix/.install.lock"
macos_acquire_install_lock "$install_lock_path" || {
  echo "another Bria installation is active" >&2
  exit 1
}
install_lock="$install_lock_path"
if [ -e "$release" ]; then
  "$release/bria" version >"$staging/existing-version.json"
  [ -d "$release" ] && [ ! -L "$release" ] &&
    cmp -s "$staging/bria" "$release/bria" &&
    cmp -s "$staging/bria-apple-speech" "$release/bria-apple-speech" &&
    cmp -s "$staging/release.json" "$staging/existing-version.json" &&
    grep -F '"schema":1' "$release/release.json" >/dev/null &&
    grep -F '"version":"'"$version"'"' "$release/release.json" >/dev/null &&
    grep -F '"commit":"'"$commit"'"' "$release/release.json" >/dev/null &&
    grep -F '"built_at":"'"$built_at"'"' "$release/release.json" >/dev/null &&
    grep -F '"binary_sha256":"'"$binary_sha256"'"' "$release/release.json" >/dev/null || {
      echo "existing immutable Bria release does not match its identity" >&2
      exit 1
    }
else
  release_stage=$(mktemp -d "$releases/.stage.XXXXXX")
  install -m 0755 "$staging/bria" "$release_stage/bria"
  install -m 0755 "$staging/bria-apple-speech" "$release_stage/bria-apple-speech"
  install -m 0444 "$staging/release.json" "$release_stage/release.json"
  mv "$release_stage" "$release"
  release_stage=""
fi

previous_target=""
prior_previous_target=""
previous_link="$install_prefix/previous"
restore_previous_pointer() {
  if [ -n "$prior_previous_target" ]; then
    previous_new="$install_prefix/.previous.rollback.$$"
    macos_atomic_symlink "$prior_previous_target" "$previous_link" "$previous_new"
    previous_new=""
  else
    rm -f "$previous_link"
  fi
}
if [ -L "$previous_link" ]; then
  prior_previous_target=$(readlink "$previous_link")
elif [ -e "$previous_link" ]; then
  echo "Bria previous activation path is not a symlink" >&2
  exit 1
fi
legacy_release=""
if [ -L "$current" ]; then
  previous_target=$(readlink "$current")
elif [ -d "$current" ]; then
  [ -x "$current/bria" ] || { echo "legacy Bria current has no executable" >&2; exit 1; }
  legacy_sha256=$(/usr/bin/shasum -a 256 "$current/bria" | awk '{print $1}')
  legacy_release="$releases/legacy-$legacy_sha256"
  [ ! -e "$legacy_release" ] || legacy_release="$releases/legacy-$built_at-$legacy_sha256"
  mv "$current" "$legacy_release"
  previous_target="$legacy_release"
elif [ -e "$current" ]; then
  echo "Bria current activation path is not a directory or symlink" >&2
  exit 1
fi
if [ -n "$previous_target" ]; then
  previous_new="$install_prefix/.previous.new.$$"
  if ! macos_atomic_symlink "$previous_target" "$previous_link" "$previous_new"; then
    [ -z "$legacy_release" ] || mv "$legacy_release" "$current"
    echo "cannot preserve previous Bria release" >&2
    exit 1
  fi
  previous_new=""
fi
current_new="$install_prefix/.current.new.$$"
if ! macos_atomic_symlink "$release" "$current" "$current_new"; then
  restore_previous_pointer || true
  [ -z "$legacy_release" ] || mv "$legacy_release" "$current"
  echo "cannot activate new Bria release" >&2
  exit 1
fi
current_new=""

if [ -r "$config" ]; then
  if ! BRIA_BINARY="$current/bria" BRIA_DATA_DIR="$data_dir" BRIA_CONFIG="$config" \
    "$install_user"; then
    if [ -n "$previous_target" ]; then
      current_new="$install_prefix/.current.rollback.$$"
      macos_atomic_symlink "$previous_target" "$current" "$current_new"
      current_new=""
      restore_previous_pointer || true
      if ! BRIA_SKIP_PROVIDER_HOOKS=1 BRIA_BINARY="$current/bria" BRIA_DATA_DIR="$data_dir" BRIA_CONFIG="$config" \
        "$install_user"; then
        echo "previous Bria release was selected but did not become ready" >&2
      fi
    fi
    echo "Bria activation failed; the previous release was restored when available" >&2
    exit 1
  fi
  echo "Bria is installed and loaded"
else
  echo "Bria is installed at $current/bria"
  echo "Create or join a cluster to produce $config, then rerun this installer"
fi
