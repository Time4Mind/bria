#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: validate-install.sh ABSOLUTE_BIN_DIR VERSION [ABSOLUTE_CONFIG]' >&2
	exit 2
}

test "$#" -eq 2 || test "$#" -eq 3 || usage
bin_dir=$1
expected_version=$2
case "$bin_dir" in /*) ;; *) usage ;; esac
test "$expected_version" != dev && test -n "$expected_version" || usage

for binary in bria bria-codex-adapter bria-claude-adapter; do
	test -f "$bin_dir/$binary" || { printf '%s\n' "validate-install: missing $binary" >&2; exit 1; }
	test -x "$bin_dir/$binary" || { printf '%s\n' "validate-install: $binary is not executable" >&2; exit 1; }
done

actual=$("$bin_dir/bria" --version)
test "$actual" = "bria $expected_version" || {
	printf '%s\n' "validate-install: version mismatch: $actual" >&2
	exit 1
}

if test "$#" -eq 3; then
	case "$3" in /*) ;; *) usage ;; esac
	"$bin_dir/bria" check-config --config "$3"
fi

printf '%s\n' "Bria installation $expected_version: OK"
