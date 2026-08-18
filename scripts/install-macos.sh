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
install_root="${BRIA_INSTALL_ROOT:-$HOME/.local/opt/bria/current}"
. "$project_root/scripts/macos-install-target.sh"
resolve_macos_install_target
data_dir="$resolved_data_dir"
config="$resolved_config"
go_binary="${GO_BINARY:-$(command -v go || true)}"

[ -n "$go_binary" ] && [ -x "$go_binary" ] || {
  echo "Go is required to build Bria; install the pinned version from go.dev and retry" >&2
  exit 1
}

staging=$(mktemp -d "${TMPDIR:-/tmp}/bria-install.XXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM
version=$(git -C "$project_root" describe --tags --always --dirty 2>/dev/null || printf development)
commit=$(git -C "$project_root" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
built_at=$(date +%s)
link_flags="-s -w -X github.com/Time4Mind/bria/internal/buildinfo.Version=$version -X github.com/Time4Mind/bria/internal/buildinfo.Commit=$commit -X github.com/Time4Mind/bria/internal/buildinfo.BuiltAt=$built_at"

(cd "$project_root" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$go_binary" build -trimpath \
  -ldflags "$link_flags" -o "$staging/bria" ./cmd/bria)
"$project_root/scripts/build-apple-speech.sh" "$staging/bria-apple-speech"
mkdir -p "$install_root" "$data_dir"
install -m 0755 "$staging/bria" "$install_root/bria"
install -m 0755 "$staging/bria-apple-speech" "$install_root/bria-apple-speech"

if [ -r "$config" ]; then
  BRIA_BINARY="$install_root/bria" BRIA_DATA_DIR="$data_dir" BRIA_CONFIG="$config" \
    "$project_root/deploy/launchd/install-user.sh"
  echo "Bria is installed and loaded"
else
  echo "Bria is installed at $install_root/bria"
  echo "Create or join a cluster to produce $config, then rerun this installer"
fi
