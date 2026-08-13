#!/bin/sh
set -eu

[ "$(uname -s)" = Darwin ] || {
  echo "build-macos-release.sh requires macOS" >&2
  exit 1
}
project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
output_root="${1:-$project_root/dist}"
version="${BRIA_VERSION:-$(git -C "$project_root" describe --tags --always --dirty)}"
commit="${BRIA_COMMIT:-$(git -C "$project_root" rev-parse --short=12 HEAD)}"
built_at="${SOURCE_DATE_EPOCH:-$(date +%s)}"
stage=$(mktemp -d "${TMPDIR:-/tmp}/bria-release.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
mkdir -p "$output_root"
link_flags="-s -w -X github.com/Time4Mind/bria/internal/buildinfo.Version=$version -X github.com/Time4Mind/bria/internal/buildinfo.Commit=$commit -X github.com/Time4Mind/bria/internal/buildinfo.BuiltAt=$built_at"
(cd "$project_root" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath \
  -ldflags "$link_flags" -o "$stage/bria" ./cmd/bria)
BRIA_APPLE_ARCH=arm64 "$project_root/scripts/build-apple-speech.sh" "$stage/bria-apple-speech"
cp "$project_root/README.md" "$stage/"
cp "$project_root/docs/operations.md" "$stage/OPERATIONS.md"
cp -R "$project_root/deploy" "$stage/"
archive="$output_root/bria_${version}_darwin_arm64.tar.gz"
COPYFILE_DISABLE=1 tar -C "$stage" -czf "$archive" .
echo "release artifact: $archive"
