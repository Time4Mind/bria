#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=${VERSION:-dev}
go_command=${GO:-go}
go_cache=${GOCACHE:-$repo_dir/.cache/go-build}
go_mod_cache=${GOMODCACHE:-$repo_dir/.cache/go-mod}

case "$version" in
	*[!0-9A-Za-z._+-]*|.*|*..*|'')
		printf '%s\n' 'build-local: VERSION contains unsupported characters' >&2
		exit 2
		;;
esac

mkdir -p "$repo_dir/bin"
staging=$(mktemp -d "${TMPDIR:-/tmp}/bria-build.XXXXXX")
trap 'rm -rf -- "$staging"' EXIT HUP INT TERM
(
	cd "$repo_dir"
	env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" \
		"$go_command" build -mod=readonly -trimpath -buildvcs=false -ldflags="-X main.version=$version" -o "$staging/bria" ./cmd/bria
	env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" \
		"$go_command" build -mod=readonly -trimpath -buildvcs=false -o "$staging/bria-codex-adapter" ./cmd/bria-codex-adapter
	env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" \
		"$go_command" build -mod=readonly -trimpath -buildvcs=false -o "$staging/bria-claude-adapter" ./cmd/bria-claude-adapter
)
test "$("$staging/bria" --version)" = "bria $version" || { printf '%s\n' 'build-local: version injection failed' >&2; exit 1; }
for binary in bria bria-codex-adapter bria-claude-adapter; do
	mv -f -- "$staging/$binary" "$repo_dir/bin/$binary"
done
