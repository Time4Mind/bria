#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=${VERSION:-}
dist_root=${DIST_DIR:-$repo_dir/dist}
source_date_epoch=${SOURCE_DATE_EPOCH:-0}
revision=${REVISION:-}
go_command=${GO:-go}
go_cache=${GOCACHE:-$repo_dir/.cache/go-build}
go_mod_cache=${GOMODCACHE:-$repo_dir/.cache/go-mod}
release_key_id=${RELEASE_KEY_ID:-}
signing_key_file=${RELEASE_SIGNING_KEY_FILE:-}
trust_file=${RELEASE_TRUST_FILE:-}

case "$version" in
	""|dev|.*|*..*|*[!0-9A-Za-z._+-]*)
		printf '%s\n' 'build-release: VERSION must be a non-dev release identifier' >&2
		exit 2
		;;
esac
case "$revision" in
	""|*[!0-9A-Za-z._+-]*)
		printf '%s\n' 'build-release: REVISION is required and must be a source identifier' >&2
		exit 2
		;;
esac
case "$release_key_id" in
	""|*[!0-9A-Za-z._+-]*)
		printf '%s\n' 'build-release: RELEASE_KEY_ID is required and invalid' >&2
		exit 2
		;;
esac
case "$signing_key_file" in /*) ;; *) printf '%s\n' 'build-release: RELEASE_SIGNING_KEY_FILE must be absolute' >&2; exit 2 ;; esac
case "$trust_file" in /*) ;; *) printf '%s\n' 'build-release: RELEASE_TRUST_FILE must be absolute' >&2; exit 2 ;; esac
case "$source_date_epoch" in *[!0-9]*|'') printf '%s\n' 'build-release: SOURCE_DATE_EPOCH must be a non-negative integer' >&2; exit 2 ;; esac
case "$dist_root" in /*) ;; *) printf '%s\n' 'build-release: DIST_DIR must be absolute' >&2; exit 2 ;; esac

output=$dist_root/$version
test ! -e "$output" || { printf '%s\n' "build-release: output already exists: $output" >&2; exit 1; }
mkdir -p "$dist_root"
staging=$(mktemp -d "${TMPDIR:-/tmp}/bria-release.XXXXXX")
cleanup() { rm -rf -- "$staging"; }
trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 1' HUP INT TERM

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
	os=${target%/*}
	arch=${target#*/}
	bundle_name=bria_${version}_${os}_${arch}
	bundle=$staging/$bundle_name
	mkdir -p "$bundle"
	(
		cd "$repo_dir"
		env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" GOOS="$os" GOARCH="$arch" \
			"$go_command" build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$version" -o "$bundle/bria" ./cmd/bria
		env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" GOOS="$os" GOARCH="$arch" \
			"$go_command" build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o "$bundle/bria-codex-adapter" ./cmd/bria-codex-adapter
		env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" GOOS="$os" GOARCH="$arch" \
			"$go_command" build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o "$bundle/bria-claude-adapter" ./cmd/bria-claude-adapter
		env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" GOOS="$os" GOARCH="$arch" \
			"$go_command" build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o "$bundle/bria-releasemanifest" ./packaging/releasemanifest
	)
	printf '{"version":"%s","revision":"%s","os":"%s","arch":"%s","source_date_epoch":%s}\n' \
		"$version" "$revision" "$os" "$arch" "$source_date_epoch" >"$bundle/release.json"
	cp "$repo_dir/packaging/render-service.sh" "$repo_dir/packaging/validate-install.sh" \
		"$repo_dir/packaging/install-release.sh" "$repo_dir/packaging/rollback-install.sh" \
		"$repo_dir/packaging/service-control.sh" "$repo_dir/packaging/postflight.sh" "$bundle/"
	if test "$os" = darwin; then
		cp "$repo_dir/packaging/macos/com.time4mind.bria.plist.tmpl" "$bundle/"
	else
		cp "$repo_dir/packaging/linux/bria.service.tmpl" "$bundle/bria.service.tmpl"
		cp "$repo_dir/packaging/wsl/bria.service.tmpl" "$bundle/bria-wsl.service.tmpl"
	fi
done

(
	cd "$repo_dir"
	env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" \
		"$go_command" run -mod=readonly ./packaging/releasepack \
		-input "$staging" -output "$output" -source-date-epoch "$source_date_epoch"
)
(
	cd "$repo_dir"
	env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" \
		"$go_command" run -mod=readonly ./packaging/releasemanifest sign \
		-release-dir "$output" -version "$version" -key-id "$release_key_id" \
		-trust-file "$trust_file"
)
"$repo_dir/packaging/verify-release.sh" "$output"
printf '%s\n' "Bria release artifacts: $output"
