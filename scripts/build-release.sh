#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_root="${1:-$project_root/dist}"
output_root="$(mkdir -p "$output_root" && cd "$output_root" && pwd)"
git_version="development"
git_commit="unknown"
if git -C "$project_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git_version="$(git -C "$project_root" describe --tags --always --dirty)"
  git_commit="$(git -C "$project_root" rev-parse --short=12 HEAD)"
fi
version="${BRIA_VERSION:-$git_version}"
commit="${BRIA_COMMIT:-$git_commit}"
built_at="${SOURCE_DATE_EPOCH:-$(date +%s)}"
link_flags="-s -w -X github.com/Time4Mind/bria/internal/buildinfo.Version=$version -X github.com/Time4Mind/bria/internal/buildinfo.Commit=$commit -X github.com/Time4Mind/bria/internal/buildinfo.BuiltAt=$built_at"

rm -f "$output_root/SHA256SUMS"

targets=(
  "linux amd64"
  "linux arm64"
  "darwin arm64"
  "windows amd64"
)

for target in "${targets[@]}"; do
  read -r target_os target_arch <<<"$target"
  package="bria_${version}_${target_os}_${target_arch}"
  stage="$(mktemp -d)"
  binary="bria"
  [ "$target_os" = windows ] && binary="bria.exe"
  trap 'rm -rf "$stage"' EXIT
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -trimpath -ldflags "$link_flags" -o "$stage/$binary" ./cmd/bria
  cp README.md "$stage/"
  cp docs/operations.md "$stage/OPERATIONS.md"
  cp -R deploy "$stage/"
  find "$stage" -exec touch -h -d "@$built_at" {} +
  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$built_at" \
    -C "$stage" -cf - . | gzip -n >"$output_root/$package.tar.gz"
  artifact="$output_root/$package.tar.gz"
  rm -rf "$stage"
  trap - EXIT
  (cd "$output_root" && sha256sum "$(basename "$artifact")") >>"$output_root/SHA256SUMS"
done

echo "release artifacts: $output_root"
