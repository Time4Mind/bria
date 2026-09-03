#!/bin/sh
set -eu
umask 077

usage() {
	printf '%s\n' 'usage: rollback-install.sh ABSOLUTE_INSTALL_ROOT ABSOLUTE_CONFIG ABSOLUTE_TRUST_FILE [ABSOLUTE_VERIFIER OPERATION_ID NODE_ID FROM_VERSION TARGET_VERSION STATE_FINGERPRINT]' >&2
	exit 2
}

test "$#" -eq 3 || test "$#" -eq 9 || usage
selected_mode=false
install_root=$1
config=$2
trust_file=$3
if test "$#" -eq 9; then
	selected_mode=true
	verifier=$4
	operation_id=$5
	node_id=$6
	from_version=$7
	target_version=$8
	state_fingerprint=$9
	case "$verifier" in /*) ;; *) usage ;; esac
	test -x "$verifier" && test -f "$verifier" && test ! -L "$verifier" || usage
	case "$operation_id" in ""|.*|*..*|*[!0-9A-Za-z._:+-]*) usage ;; esac
	case "$node_id" in ""|.*|*..*|*[!0-9A-Za-z._:+-]*) usage ;; esac
	case "$from_version" in ""|dev|.*|*..*|*[!0-9A-Za-z._+-]*) usage ;; esac
	case "$target_version" in ""|dev|.*|*..*|*[!0-9A-Za-z._+-]*) usage ;; esac
	case "$state_fingerprint" in ""|.*|*..*|*[!0-9A-Za-z._:+-]*) usage ;; esac
fi
case "$install_root" in /*) ;; *) usage ;; esac
case "$config" in /*) ;; *) usage ;; esac
case "$trust_file" in /*) ;; *) usage ;; esac
test -L "$install_root/current" || { printf '%s\n' 'rollback-install: current version is required' >&2; exit 1; }
current_target=$(readlink "$install_root/current")
if test "$selected_mode" = true; then
	operation_receipts=$install_root/update-operation-receipts
	operation_receipt=$operation_receipts/$operation_id
	mkdir -p "$operation_receipts"
	if test -e "$operation_receipt" || test -L "$operation_receipt"; then
		test -f "$operation_receipt" && test ! -L "$operation_receipt" || { printf '%s\n' 'rollback-install: operation receipt is invalid' >&2; exit 1; }
		test "$(wc -l <"$operation_receipt" | tr -d ' ')" -eq 5 || { printf '%s\n' 'rollback-install: operation receipt is malformed' >&2; exit 1; }
		confirmed_version=$(sed -n '4p' "$operation_receipt")
		test "$(sed -n '1p' "$operation_receipt")" = "$operation_id" && \
			test "$(sed -n '2p' "$operation_receipt")" = "$node_id" && \
			test "$(sed -n '3p' "$operation_receipt")" = "$from_version" && \
			test "$confirmed_version" = "$target_version" && \
			test "$(sed -n '5p' "$operation_receipt")" = "$state_fingerprint" || { printf '%s\n' 'rollback-install: operation identity was reused' >&2; exit 1; }
		test "$current_target" = "releases/$confirmed_version" || { printf '%s\n' 'rollback-install: confirmed operation no longer matches current version' >&2; exit 1; }
		printf '%s\n' "Bria rollback operation already confirmed"
		exit 0
	fi
	operation_pending=$operation_receipts/.pending-$operation_id
	if test -e "$operation_pending" || test -L "$operation_pending"; then
		test -f "$operation_pending" && test ! -L "$operation_pending" || { printf '%s\n' 'rollback-install: pending operation receipt is invalid' >&2; exit 1; }
		test "$(wc -l <"$operation_pending" | tr -d ' ')" -eq 5 && \
			test "$(sed -n '1p' "$operation_pending")" = "$operation_id" && \
			test "$(sed -n '2p' "$operation_pending")" = "$node_id" && \
			test "$(sed -n '3p' "$operation_pending")" = "$from_version" && \
			test "$(sed -n '4p' "$operation_pending")" = "$target_version" && \
			test "$(sed -n '5p' "$operation_pending")" = "$state_fingerprint" || { printf '%s\n' 'rollback-install: pending operation identity mismatch' >&2; exit 1; }
	else
		operation_pending_next=$operation_receipts/.pending-next-${operation_id}.$$
		printf '%s\n%s\n%s\n%s\n%s\n' "$operation_id" "$node_id" "$from_version" "$target_version" "$state_fingerprint" >"$operation_pending_next"
		chmod 0600 "$operation_pending_next"
		mv -- "$operation_pending_next" "$operation_pending"
		sync
	fi
	if test "$current_target" = "releases/$target_version"; then
		case "$(uname -s)" in Darwin) noop_os=darwin ;; Linux) noop_os=linux ;; *) exit 1 ;; esac
		case "$(uname -m)" in x86_64|amd64) noop_arch=amd64 ;; arm64|aarch64) noop_arch=arm64 ;; *) exit 1 ;; esac
		noop_receipt=$install_root/release-receipts/$target_version
		noop_archive=$noop_receipt/bria_${target_version}_${noop_os}_${noop_arch}.tar.gz
		"$verifier" verify-installed -manifest "$noop_receipt/release-manifest.json" -artifact "$noop_archive" \
			-installed "$install_root/releases/$target_version" -version "$target_version" \
			-platform "$noop_os" -arch "$noop_arch" -trust-file "$trust_file" >/dev/null
		test ! -e "$operation_receipt" && test ! -L "$operation_receipt" || { printf '%s\n' 'rollback-install: operation receipt appeared concurrently' >&2; exit 1; }
		mv -- "$operation_pending" "$operation_receipt"
		sync
		printf '%s\n' "Bria rollback to $target_version required no pointer change"
		exit 0
	fi
fi
test -L "$install_root/previous" || { printf '%s\n' 'rollback-install: previous version is required' >&2; exit 1; }
previous_target=$(readlink "$install_root/previous")
replace_link() {
	case "$(uname -s)" in
		Darwin) mv -fh -- "$1" "$2" ;;
		Linux) mv -fT -- "$1" "$2" ;;
		*) printf '%s\n' 'rollback-install: unsupported operating system' >&2; exit 1 ;;
	esac
}
valid_release_target() {
	case "$1" in
		releases/[0-9A-Za-z._+-]* )
			case "${1#releases/}" in ""|.*|*..*|*/*|*[!0-9A-Za-z._+-]*) return 1 ;; esac
			return 0
			;;
	esac
	return 1
}
valid_release_target "$current_target" || { printf '%s\n' 'rollback-install: current target is unmanaged' >&2; exit 1; }
valid_release_target "$previous_target" || { printf '%s\n' 'rollback-install: previous target is unmanaged' >&2; exit 1; }
transaction=$install_root/.rollback-transaction
quarantine=$install_root/.rollback-replaced
if test -e "$transaction" || test -L "$transaction"; then
	test -f "$transaction" && test ! -L "$transaction" || { printf '%s\n' 'rollback-install: recovery marker is invalid' >&2; exit 1; }
	test "$(wc -l <"$transaction" | tr -d ' ')" -eq 2 || { printf '%s\n' 'rollback-install: recovery marker is malformed' >&2; exit 1; }
	old_current=$(sed -n '1p' "$transaction")
	old_previous=$(sed -n '2p' "$transaction")
	valid_release_target "$old_current" && valid_release_target "$old_previous" && test "$old_current" != "$old_previous" || { printf '%s\n' 'rollback-install: recovery marker targets are invalid' >&2; exit 1; }
	case "$current_target" in "$old_current"|"$old_previous") ;; *) printf '%s\n' 'rollback-install: current conflicts with recovery marker' >&2; exit 1 ;; esac
	case "$previous_target" in "$old_current"|"$old_previous") ;; *) printf '%s\n' 'rollback-install: previous conflicts with recovery marker' >&2; exit 1 ;; esac
	test -d "$install_root/$old_previous" || { printf '%s\n' 'rollback-install: recovered bundle is unavailable' >&2; exit 1; }
	recovery_current=$install_root/.current.recover.$$
	recovery_previous=$install_root/.previous.recover.$$
	recovery_cleanup() { rm -f -- "$recovery_current" "$recovery_previous"; }
	trap recovery_cleanup EXIT
	trap 'trap - EXIT; recovery_cleanup; exit 1' HUP INT TERM
	ln -s "$old_previous" "$recovery_current"
	ln -s "$old_current" "$recovery_previous"
	replace_link "$recovery_current" "$install_root/current"
	replace_link "$recovery_previous" "$install_root/previous"
	sync
	if test "$selected_mode" = true; then
		recovery_version=${old_previous#releases/}
		test "$recovery_version" = "$target_version" && test "$old_current" = "releases/$from_version" || { printf '%s\n' 'rollback-install: recovery operation identity mismatch' >&2; exit 1; }
		case "$(uname -s)" in Darwin) recovery_os=darwin ;; Linux) recovery_os=linux ;; *) exit 1 ;; esac
		case "$(uname -m)" in x86_64|amd64) recovery_arch=amd64 ;; arm64|aarch64) recovery_arch=arm64 ;; *) exit 1 ;; esac
		recovery_receipt=$install_root/release-receipts/$recovery_version
		recovery_archive=$recovery_receipt/bria_${recovery_version}_${recovery_os}_${recovery_arch}.tar.gz
		"$verifier" verify-installed -manifest "$recovery_receipt/release-manifest.json" -artifact "$recovery_archive" \
			-installed "$install_root/releases/$recovery_version" -version "$recovery_version" \
			-platform "$recovery_os" -arch "$recovery_arch" -trust-file "$trust_file" >/dev/null
		test ! -e "$operation_receipt" && test ! -L "$operation_receipt" || { printf '%s\n' 'rollback-install: operation receipt appeared concurrently' >&2; exit 1; }
		mv -- "$operation_pending" "$operation_receipt"
	fi
	rm -f -- "$transaction"
	if test -e "$quarantine"; then
		test -d "$quarantine" && test ! -L "$quarantine" || { printf '%s\n' 'rollback-install: quarantine is invalid' >&2; exit 1; }
		rm -rf -- "$quarantine"
	fi
	sync
	trap - EXIT HUP INT TERM
	printf '%s\n' 'Bria interrupted rollback completed; service restart is still required'
	exit 0
fi
test "$current_target" != "$previous_target" || { printf '%s\n' 'rollback-install: current and previous are identical' >&2; exit 1; }

version=${previous_target#releases/}
if test "$selected_mode" = true; then
	test "$current_target" = "releases/$from_version" && test "$version" = "$target_version" || { printf '%s\n' 'rollback-install: requested versions do not match managed pointers' >&2; exit 1; }
fi
previous_dir=$install_root/$previous_target
if test -e "$quarantine" || test -L "$quarantine"; then
	test -d "$quarantine" && test ! -L "$quarantine" || { printf '%s\n' 'rollback-install: quarantine is invalid' >&2; exit 1; }
	if test -e "$previous_dir"; then
		rm -rf -- "$quarantine"
	else
		mv -- "$quarantine" "$previous_dir"
	fi
fi
receipt=$install_root/release-receipts/$version
test -d "$receipt" || { printf '%s\n' 'rollback-install: signed previous release receipt is unavailable' >&2; exit 1; }
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

case "$(uname -s)" in
	Darwin) artifact_os=darwin ;;
	Linux) artifact_os=linux ;;
	*) printf '%s\n' 'rollback-install: unsupported operating system' >&2; exit 1 ;;
esac
case "$(uname -m)" in
	x86_64|amd64) artifact_arch=amd64 ;;
	arm64|aarch64) artifact_arch=arm64 ;;
	*) printf '%s\n' 'rollback-install: unsupported architecture' >&2; exit 1 ;;
esac
bundle=bria_${version}_${artifact_os}_${artifact_arch}
archive=$receipt/$bundle.tar.gz
if test "$selected_mode" = true; then
	"$verifier" verify-artifact -manifest "$receipt/release-manifest.json" -artifact "$archive" \
		-version "$version" -platform "$artifact_os" -arch "$artifact_arch" -trust-file "$trust_file" >/dev/null
else
	RELEASE_TRUST_FILE=$trust_file "$script_dir/verify-release.sh" "$receipt" >/dev/null
fi
staging=$(mktemp -d "$install_root/.rollback.XXXXXX")

current_next=$install_root/.current.rollback.$$
previous_next=$install_root/.previous.rollback.$$
transaction_next=$install_root/.rollback-transaction.next.$$
cleanup() {
	rm -rf -- "$staging"
	rm -f -- "$current_next" "$previous_next" "$transaction_next"
}
trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 1' HUP INT TERM
tar -xzf "$archive" -C "$staging"
test -d "$staging/$bundle" || { printf '%s\n' 'rollback-install: archive root is invalid' >&2; exit 1; }
set -- "$staging"/*
test "$#" -eq 1 && test "$1" = "$staging/$bundle" || { printf '%s\n' 'rollback-install: archive has unexpected roots' >&2; exit 1; }
if find "$staging/$bundle" -type l -print | grep -q .; then
	printf '%s\n' 'rollback-install: archive contains a symbolic link' >&2
	exit 1
fi
if find "$staging/$bundle" ! -type d ! -type f -print | grep -q .; then
	printf '%s\n' 'rollback-install: archive contains a special file' >&2
	exit 1
fi
if test "$selected_mode" = true; then
	"$verifier" verify-artifact -manifest "$receipt/release-manifest.json" -artifact "$archive" \
		-version "$version" -platform "$artifact_os" -arch "$artifact_arch" -trust-file "$trust_file" >/dev/null
else
	RELEASE_TRUST_FILE=$trust_file "$script_dir/verify-release.sh" "$receipt" >/dev/null
fi
"$staging/$bundle/validate-install.sh" "$staging/$bundle" "$version" "$config" >/dev/null

test ! -e "$quarantine" || { printf '%s\n' 'rollback-install: stale rollback quarantine exists' >&2; exit 1; }
mv -- "$previous_dir" "$quarantine"
if ! mv -- "$staging/$bundle" "$previous_dir"; then
	mv -- "$quarantine" "$previous_dir"
	printf '%s\n' 'rollback-install: cannot replace previous bundle' >&2
	exit 1
fi
umask 077
printf '%s\n%s\n' "$current_target" "$previous_target" >"$transaction_next"
mv -- "$transaction_next" "$transaction"
sync
ln -s "$previous_target" "$current_next"
ln -s "$current_target" "$previous_next"
replace_link "$current_next" "$install_root/current"
replace_link "$previous_next" "$install_root/previous"
sync
rm -f -- "$transaction"
rm -rf -- "$quarantine"
sync
if test "$selected_mode" = true; then
	test ! -e "$operation_receipt" && test ! -L "$operation_receipt" || { printf '%s\n' 'rollback-install: operation receipt appeared concurrently' >&2; exit 1; }
	mv -- "$operation_pending" "$operation_receipt"
	sync
fi
cleanup
trap - EXIT HUP INT TERM
printf '%s\n' "Bria rolled back to $version; service restart is still required"
