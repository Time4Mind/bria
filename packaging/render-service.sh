#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: render-service.sh macos|linux|wsl ABSOLUTE_BIN ABSOLUTE_CONFIG ABSOLUTE_LOG_DIR OUTPUT' >&2
	exit 2
}

test "$#" -eq 5 || usage
platform=$1
bria_bin=$2
bria_config=$3
bria_log_dir=$4
output=$5

case "$platform" in
	macos)
		repository_template=packaging/macos/com.time4mind.bria.plist.tmpl
		bundle_template=com.time4mind.bria.plist.tmpl
		;;
	linux)
		repository_template=packaging/linux/bria.service.tmpl
		bundle_template=bria.service.tmpl
		;;
	wsl)
		repository_template=packaging/wsl/bria.service.tmpl
		bundle_template=bria-wsl.service.tmpl
		;;
	*) usage ;;
esac

for path in "$bria_bin" "$bria_config" "$bria_log_dir" "$output"; do
	case "$path" in
		/*) ;;
		*) printf '%s\n' "render-service: path must be absolute: $path" >&2; exit 2 ;;
	esac
	case "$path" in
		*[!A-Za-z0-9_./+-]*)
			printf '%s\n' "render-service: path contains a character unsafe for service templates: $path" >&2
			exit 2
			;;
	esac
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
template=$repo_dir/$repository_template
if test ! -f "$template"; then
	template=$script_dir/$bundle_template
fi
test -f "$template" || { printf '%s\n' "render-service: missing $platform service template" >&2; exit 1; }
test -d "$(dirname -- "$output")" || { printf '%s\n' 'render-service: output directory does not exist' >&2; exit 1; }

escape_sed() {
	printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}

bin_escaped=$(escape_sed "$bria_bin")
config_escaped=$(escape_sed "$bria_config")
log_escaped=$(escape_sed "$bria_log_dir")
umask 077
temporary=$(mktemp "${output}.tmp.XXXXXX")
cleanup() { rm -f -- "$temporary"; }
trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 1' HUP INT TERM
sed \
	-e "s|@BRIA_BIN@|$bin_escaped|g" \
	-e "s|@BRIA_CONFIG@|$config_escaped|g" \
	-e "s|@BRIA_LOG_DIR@|$log_escaped|g" \
	"$template" >"$temporary"
if grep -Eq '@BRIA_(BIN|CONFIG|LOG_DIR)@' "$temporary"; then
	printf '%s\n' 'render-service: unresolved placeholder' >&2
	exit 1
fi
chmod 0600 "$temporary"
mv -f -- "$temporary" "$output"
trap - EXIT HUP INT TERM
