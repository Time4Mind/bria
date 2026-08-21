#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$project_root/scripts/build-provenance.sh"
require_clean_tracked_tree "$project_root"
source_head="$(git -C "$project_root" rev-parse HEAD)"
output_root="${1:-$project_root/dist}"
output_root="$(mkdir -p "$output_root" && cd "$output_root" && pwd)"
cd "$project_root"
git_version="development"
git_commit="unknown"
if git -C "$project_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git_version="$(git -C "$project_root" describe --tags --always --dirty)"
  git_commit="$(git -C "$project_root" rev-parse HEAD)"
fi
version="${BRIA_VERSION:-$git_version}"
commit="${BRIA_COMMIT:-$git_commit}"
if [[ -n "${BRIA_COMMIT:-}" && "$git_commit" != unknown && "$commit" != "$git_commit" ]]; then
  echo "BRIA_COMMIT does not match HEAD" >&2
  exit 1
fi
validate_build_version "$version"
validate_build_commit "$commit"
built_at="${SOURCE_DATE_EPOCH:-$(date +%s)}"
validate_build_timestamp "$built_at"
link_flags="-s -w -X github.com/Time4Mind/bria/internal/buildinfo.Version=$version -X github.com/Time4Mind/bria/internal/buildinfo.Commit=$commit -X github.com/Time4Mind/bria/internal/buildinfo.BuiltAt=$built_at"

rm -f "$output_root/SHA256SUMS"

if [ -n "${BRIA_TARGETS:-}" ]; then
  IFS=';' read -r -a targets <<<"$BRIA_TARGETS"
else
  targets=(
    "linux amd64"
    "linux arm64"
    "darwin arm64"
    "windows amd64"
  )
fi

for target in "${targets[@]}"; do
  verify_build_snapshot "$project_root" "$source_head"
  read -r target_os target_arch <<<"$target"
  package="bria_${version}_${target_os}_${target_arch}"
  stage="$(mktemp -d)"
  temporary_artifact=""
  binary="bria"
  [ "$target_os" = windows ] && binary="bria.exe"
  trap 'rm -rf "$stage"; [ -z "${temporary_artifact:-}" ] || rm -f "$temporary_artifact"' EXIT
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -trimpath -ldflags "$link_flags" -o "$stage/$binary" ./cmd/bria
  verify_build_snapshot "$project_root" "$source_head"
  cp README.md "$stage/"
  cp docs/operations.md "$stage/OPERATIONS.md"
  cp -R deploy "$stage/"
  verify_build_snapshot "$project_root" "$source_head"
  find "$stage" -exec touch -h -d "@$built_at" {} +
  temporary_artifact="$output_root/.$package.tar.gz.tmp.$$"
  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$built_at" \
    -C "$stage" -cf - . | gzip -n >"$temporary_artifact"
  verify_build_snapshot "$project_root" "$source_head"
  mv "$temporary_artifact" "$output_root/$package.tar.gz"
  temporary_artifact=""
  artifact="$output_root/$package.tar.gz"
  rm -rf "$stage"
  trap - EXIT
  (cd "$output_root" && sha256sum "$(basename "$artifact")") >>"$output_root/SHA256SUMS"
done

echo "release artifacts: $output_root"
