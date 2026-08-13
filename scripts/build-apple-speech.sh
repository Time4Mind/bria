#!/bin/sh
set -eu

if [ "$(uname -s)" != "Darwin" ]; then
    echo "bria-apple-speech can only be built on macOS" >&2
    exit 1
fi

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(dirname -- "$script_dir")
source_file="$project_dir/cmd/bria-apple-speech/main.swift"
plist_file="$project_dir/cmd/bria-apple-speech/Info.plist"
target=${1:-"$HOME/.local/bin/bria-apple-speech"}
target_dir=$(dirname -- "$target")
staging=$(mktemp -d "${TMPDIR:-/tmp}/bria-apple-speech.XXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM

mkdir -p "$target_dir"
xcrun swiftc -O -framework Speech -framework Foundation \
    -Xlinker -sectcreate -Xlinker __TEXT -Xlinker __info_plist -Xlinker "$plist_file" \
    "$source_file" -o "$staging/bria-apple-speech"
codesign --force --sign - "$staging/bria-apple-speech"
install -m 0755 "$staging/bria-apple-speech" "$target"

echo "installed $target"
echo "run '$target --authorize' once from the macOS user session"
