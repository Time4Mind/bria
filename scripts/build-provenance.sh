#!/bin/sh

require_clean_tracked_tree() {
  build_root=$1
  if ! git -C "$build_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "Bria provenance builds require an exact Git checkout" >&2
    return 1
  fi
  git -C "$build_root" diff --quiet --ignore-submodules -- || {
    echo "tracked Bria changes must be committed before a provenance build" >&2
    return 1
  }
  git -C "$build_root" diff --cached --quiet --ignore-submodules -- || {
    echo "staged Bria changes must be committed before a provenance build" >&2
    return 1
  }
  untracked=$(git -C "$build_root" ls-files --others --exclude-standard | while IFS= read -r path; do
    if [ "$path" = AGENTS.md ] || [ "${path#.bria-inbox/}" != "$path" ] ||
      [ "${path#.agents/}" != "$path" ] || [ "${path#.codex/}" != "$path" ]; then
      continue
    fi
    printf '%s\n' "$path"
  done)
  [ -z "$untracked" ] || {
    echo "untracked Bria build inputs must be committed before a provenance build" >&2
    return 1
  }
}

verify_build_snapshot() {
  build_root=$1
  expected_head=$2
  require_clean_tracked_tree "$build_root" || return 1
  [ "$(git -C "$build_root" rev-parse HEAD)" = "$expected_head" ] || {
    echo "Bria source HEAD changed while building" >&2
    return 1
  }
}

validate_build_version() {
  case "$1" in
    ''|*[!A-Za-z0-9._+-]*) echo "invalid Bria build version" >&2; return 1 ;;
  esac
  [ "${#1}" -le 128 ] || { echo "Bria build version is too long" >&2; return 1; }
}

validate_build_commit() {
  value=$1
  [ "$value" = unknown ] && return 0
  [ "${#value}" -eq 40 ] || { echo "Bria build commit must be full SHA-1" >&2; return 1; }
  case "$value" in *[!0-9a-f]*) echo "invalid Bria build commit" >&2; return 1 ;; esac
}

validate_build_timestamp() {
  value=$1
  [ -n "$value" ] && [ "${#value}" -le 20 ] || {
    echo "invalid Bria build timestamp" >&2
    return 1
  }
  case "$value" in *[!0-9]*) echo "invalid Bria build timestamp" >&2; return 1 ;; esac
}
