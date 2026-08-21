#!/bin/sh
set -eu

[ "$(uname -s)" = Darwin ] || {
  echo "build-macos-release.sh requires macOS" >&2
  exit 1
}
project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
. "$project_root/scripts/build-provenance.sh"
require_clean_tracked_tree "$project_root"
source_head=$(git -C "$project_root" rev-parse HEAD)
output_root="${1:-$project_root/dist}"
version="${BRIA_VERSION:-$(git -C "$project_root" describe --tags --always --dirty)}"
commit="${BRIA_COMMIT:-$(git -C "$project_root" rev-parse HEAD)}"
[ -z "${BRIA_COMMIT:-}" ] || [ "$commit" = "$(git -C "$project_root" rev-parse HEAD)" ] || {
  echo "BRIA_COMMIT does not match HEAD" >&2
  exit 1
}
validate_build_version "$version"
validate_build_commit "$commit"
built_at="${SOURCE_DATE_EPOCH:-$(date +%s)}"
validate_build_timestamp "$built_at"
stage=$(mktemp -d "${TMPDIR:-/tmp}/bria-release.XXXXXX")
temporary_archive=""
trap 'rm -rf "$stage"; [ -z "$temporary_archive" ] || rm -f "$temporary_archive"' EXIT HUP INT TERM
mkdir -p "$output_root"
link_flags="-s -w -X github.com/Time4Mind/bria/internal/buildinfo.Version=$version -X github.com/Time4Mind/bria/internal/buildinfo.Commit=$commit -X github.com/Time4Mind/bria/internal/buildinfo.BuiltAt=$built_at"
(cd "$project_root" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath \
  -ldflags "$link_flags" -o "$stage/bria" ./cmd/bria)
BRIA_APPLE_ARCH=arm64 "$project_root/scripts/build-apple-speech.sh" "$stage/bria-apple-speech"
verify_build_snapshot "$project_root" "$source_head"
binary_sha256=$(/usr/bin/shasum -a 256 "$stage/bria" | awk '{print $1}')
version_json=$("$stage/bria" version)
printf '%s' "$version_json" | grep -F '"version":"'"$version"'"' >/dev/null
printf '%s' "$version_json" | grep -F '"commit":"'"$commit"'"' >/dev/null
printf '%s' "$version_json" | grep -F '"built_at":"'"$built_at"'"' >/dev/null
printf '%s' "$version_json" | grep -F '"binary_sha256":"'"$binary_sha256"'"' >/dev/null
cp "$project_root/README.md" "$stage/"
cp "$project_root/docs/operations.md" "$stage/OPERATIONS.md"
cp -R "$project_root/deploy" "$stage/"
verify_build_snapshot "$project_root" "$source_head"
archive="$output_root/bria_${version}_darwin_arm64.tar.gz"
temporary_archive="$output_root/.bria_${version}_darwin_arm64.tar.gz.tmp.$$"
COPYFILE_DISABLE=1 tar -C "$stage" -czf "$temporary_archive" .
verify_build_snapshot "$project_root" "$source_head"
mv "$temporary_archive" "$archive"
temporary_archive=""
echo "release artifact: $archive"
