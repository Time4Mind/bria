#!/bin/sh
set -eu

if test "$#" -eq 0; then
	root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
elif test "$#" -eq 1; then
	case "$1" in /*) root=$1 ;; *) printf '%s\n' 'verify-supply-chain: root must be absolute' >&2; exit 2 ;; esac
else
	printf '%s\n' 'usage: verify-supply-chain.sh [ABSOLUTE_REPOSITORY_ROOT]' >&2
	exit 2
fi

test -f "$root/Dockerfile" || { printf '%s\n' 'verify-supply-chain: Dockerfile is missing' >&2; exit 1; }
for argument in GO_IMAGE RUNTIME_IMAGE; do
	image=$(sed -n "s/^ARG $argument=//p" "$root/Dockerfile")
	case "$image" in
		?*:?*@sha256:????????????????????????????????????????????????????????????????) ;;
		*) printf '%s\n' "verify-supply-chain: $argument must include an immutable sha256 digest" >&2; exit 1 ;;
	esac
	digest=${image##*@sha256:}
	case "$digest" in *[!0-9a-f]*|'') printf '%s\n' "verify-supply-chain: $argument digest is malformed" >&2; exit 1 ;; esac
done

found_action=false
for workflow in "$root"/.github/workflows/*.yml; do
	test -f "$workflow" || continue
	while IFS= read -r reference; do
		test -n "$reference" || continue
		found_action=true
		case "$reference" in *'# v'[0-9]*) ;; *) printf '%s\n' "verify-supply-chain: pinned action lacks a readable version comment: $reference" >&2; exit 1 ;; esac
		action=$(printf '%s\n' "$reference" | sed 's/[[:space:]]*#.*$//')
		case "$action" in
			actions/*@????????????????????????????????????????)
				revision=${action##*@}
				case "$revision" in *[!0-9a-f]*) printf '%s\n' "verify-supply-chain: mutable or malformed action reference: $action" >&2; exit 1 ;; esac
				;;
			*) printf '%s\n' "verify-supply-chain: mutable or unsupported action reference: $action" >&2; exit 1 ;;
		esac
	done <<EOF
$(sed -n 's/^[[:space:]]*-[[:space:]]*uses:[[:space:]]*//p' "$workflow")
EOF
done
test "$found_action" = true || { printf '%s\n' 'verify-supply-chain: no external actions found' >&2; exit 1; }
printf '%s\n' 'Immutable release supply chain: OK'
