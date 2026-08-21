#!/bin/sh
set -eu

binary=${1:-}
shift || true
case "$binary" in /*) ;; *) echo "Bria activation executable must be absolute" >&2; exit 1 ;; esac
[ -f "$binary" ] && [ ! -L "$binary" ] && [ -x "$binary" ] || {
  echo "Bria current executable is unavailable" >&2
  exit 1
}
identity=$(/usr/bin/shasum -a 256 "$binary" | awk '{print $1}')
[ "${#identity}" -eq 64 ] || {
  echo "Bria current executable identity is unavailable" >&2
  exit 1
}
export BRIA_EXPECTED_BINARY_SHA256="$identity"
exec "$binary" "$@"
