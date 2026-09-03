#!/bin/sh
set -eu
umask 077

usage() {
	printf '%s\n' 'usage: install-release.sh PLATFORM ARCH RELEASE_DIR TRUST INSTALL_ROOT CONFIG | PLATFORM ARCH VERSION STAGED_RELEASE TRUST INSTALL_ROOT CONFIG VERIFIER OPERATION_ID NODE_ID FROM_VERSION STATE_FINGERPRINT' >&2
	exit 2
}

test "$#" -eq 6 || test "$#" -eq 12 || usage
selected_mode=false
platform=$1
arch=$2
if test "$#" -eq 6; then
	release_dir=$3
	trust_file=$4
	install_root=$5
	config=$6
else
	selected_mode=true
	version=$3
	release_dir=$4
	trust_file=$5
	install_root=$6
	config=$7
	verifier=$8
	operation_id=$9
	node_id=${10}
	from_version=${11}
	state_fingerprint=${12}
fi

case "$platform" in macos) artifact_os=darwin ;; linux|wsl) artifact_os=linux ;; *) usage ;; esac
case "$arch" in amd64|arm64) ;; *) usage ;; esac
for path in "$release_dir" "$trust_file" "$install_root" "$config"; do
	case "$path" in /*) ;; *) usage ;; esac
done
if test "$selected_mode" = false; then
	version=${release_dir##*/}
else
	case "$verifier" in /*) ;; *) usage ;; esac
	test -x "$verifier" && test -f "$verifier" && test ! -L "$verifier" || usage
	case "$operation_id" in ""|.*|*..*|*[!0-9A-Za-z._:+-]*) usage ;; esac
	case "$node_id" in ""|.*|*..*|*[!0-9A-Za-z._:+-]*) usage ;; esac
	case "$from_version" in ""|dev|.*|*..*|*[!0-9A-Za-z._+-]*) usage ;; esac
	case "$state_fingerprint" in ""|.*|*..*|*[!0-9A-Za-z._:+-]*) usage ;; esac
fi
case "$version" in ""|dev|.*|*..*|*[!0-9A-Za-z._+-]*) usage ;; esac
test -d "$release_dir" || { printf '%s\n' 'install-release: release directory is unavailable' >&2; exit 1; }
test -d "$install_root" || { printf '%s\n' 'install-release: install root must already exist' >&2; exit 1; }

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
archive_name=bria_${version}_${artifact_os}_${arch}.tar.gz
verify_staged_release() {
	if test "$selected_mode" = true; then
		"$verifier" verify-artifact -manifest "$1/release-manifest.json" -artifact "$1/$archive_name" \
			-version "$version" -platform "$artifact_os" -arch "$arch" -trust-file "$trust_file" >/dev/null
	else
		RELEASE_TRUST_FILE=$trust_file "$script_dir/verify-release.sh" "$1" >/dev/null
	fi
}
verify_staged_release "$release_dir"

bundle=bria_${version}_${artifact_os}_${arch}
releases=$install_root/releases
candidate=$releases/$version
receipts=$install_root/release-receipts
receipt=$receipts/$version
mkdir -p "$releases" "$receipts"
if test "$selected_mode" = true; then
	operation_receipts=$install_root/update-operation-receipts
	operation_receipt=$operation_receipts/$operation_id
	mkdir -p "$operation_receipts"
	if test -e "$operation_receipt" || test -L "$operation_receipt"; then
		test -f "$operation_receipt" && test ! -L "$operation_receipt" || { printf '%s\n' 'install-release: operation receipt is invalid' >&2; exit 1; }
		test "$(wc -l <"$operation_receipt" | tr -d ' ')" -eq 5 || { printf '%s\n' 'install-release: operation receipt is malformed' >&2; exit 1; }
		test "$(sed -n '1p' "$operation_receipt")" = "$operation_id" && \
			test "$(sed -n '2p' "$operation_receipt")" = "$node_id" && \
			test "$(sed -n '3p' "$operation_receipt")" = "$from_version" && \
			test "$(sed -n '4p' "$operation_receipt")" = "$version" && \
			test "$(sed -n '5p' "$operation_receipt")" = "$state_fingerprint" || { printf '%s\n' 'install-release: operation identity was reused' >&2; exit 1; }
		test -L "$install_root/current" && test "$(readlink "$install_root/current")" = "releases/$version" || { printf '%s\n' 'install-release: confirmed operation no longer matches current version' >&2; exit 1; }
		printf '%s\n' "Bria $version install operation already confirmed"
		exit 0
	fi
	operation_pending=$operation_receipts/.pending-$operation_id
	if test -e "$operation_pending" || test -L "$operation_pending"; then
		test -f "$operation_pending" && test ! -L "$operation_pending" || { printf '%s\n' 'install-release: pending operation receipt is invalid' >&2; exit 1; }
		test "$(wc -l <"$operation_pending" | tr -d ' ')" -eq 5 && \
			test "$(sed -n '1p' "$operation_pending")" = "$operation_id" && \
			test "$(sed -n '2p' "$operation_pending")" = "$node_id" && \
			test "$(sed -n '3p' "$operation_pending")" = "$from_version" && \
			test "$(sed -n '4p' "$operation_pending")" = "$version" && \
			test "$(sed -n '5p' "$operation_pending")" = "$state_fingerprint" || { printf '%s\n' 'install-release: pending operation identity mismatch' >&2; exit 1; }
	else
		operation_pending_next=$operation_receipts/.pending-next-${operation_id}.$$
		printf '%s\n%s\n%s\n%s\n%s\n' "$operation_id" "$node_id" "$from_version" "$version" "$state_fingerprint" >"$operation_pending_next"
		chmod 0600 "$operation_pending_next"
		mv -- "$operation_pending_next" "$operation_pending"
		sync
	fi
fi
staging=$(mktemp -d "$install_root/.candidate.XXXXXX")
extracted=$staging/extracted
mkdir -p "$extracted"
current_next=$install_root/.current.next.$$
previous_next=$install_root/.previous.next.$$
cleanup() {
	rm -rf -- "$staging"
	rm -f -- "$current_next" "$previous_next"
}
trap cleanup EXIT
trap 'trap - EXIT; cleanup; exit 1' HUP INT TERM

replace_link() {
	if test "$platform" = macos; then
		mv -fh -- "$1" "$2"
	else
		mv -fT -- "$1" "$2"
	fi
}

receipt_was_persisted=false
if test -e "$receipt"; then
	test -d "$receipt" && test ! -L "$receipt" || { printf '%s\n' 'install-release: saved receipt is not a managed directory' >&2; exit 1; }
	cmp -s "$release_dir/release-manifest.json" "$receipt/release-manifest.json" || { printf '%s\n' 'install-release: saved receipt identifies another release' >&2; exit 1; }
	staged_receipt=$receipt
	receipt_was_persisted=true
else
	staged_receipt=$staging/receipt/$version
	mkdir -p "$staged_receipt"
	cp "$release_dir/release-manifest.json" "$staged_receipt/release-manifest.json"
	if test "$selected_mode" = true; then
		cp "$release_dir/$archive_name" "$staged_receipt/$archive_name"
	else
		for signed_archive in \
			"bria_${version}_darwin_amd64.tar.gz" \
			"bria_${version}_darwin_arm64.tar.gz" \
			"bria_${version}_linux_amd64.tar.gz" \
			"bria_${version}_linux_arm64.tar.gz"
		do
			cp "$release_dir/$signed_archive" "$staged_receipt/$signed_archive"
		done
	fi
fi
verify_staged_release "$staged_receipt"

archive=$staged_receipt/$archive_name
tar -xzf "$archive" -C "$extracted"
test -d "$extracted/$bundle" || { printf '%s\n' 'install-release: archive root is invalid' >&2; exit 1; }
set -- "$extracted"/*
test "$#" -eq 1 && test "$1" = "$extracted/$bundle" || { printf '%s\n' 'install-release: archive has unexpected roots' >&2; exit 1; }
if find "$extracted/$bundle" -type l -print | grep -q .; then
	printf '%s\n' 'install-release: archive contains a symbolic link' >&2
	exit 1
fi
if find "$extracted/$bundle" ! -type d ! -type f -print | grep -q .; then
	printf '%s\n' 'install-release: archive contains a special file' >&2
	exit 1
fi

# Close the verify/extract time-of-check gap before any installed pointer moves.
verify_staged_release "$staged_receipt"
"$extracted/$bundle/validate-install.sh" "$extracted/$bundle" "$version" "$config" >/dev/null
if test "$receipt_was_persisted" = false; then
	mv -- "$staged_receipt" "$receipt"
fi
if test -e "$candidate" || test -L "$candidate"; then
	printf '%s\n' 'install-release: version is already installed' >&2
	exit 1
fi
mv -- "$extracted/$bundle" "$candidate"
sync

if test -e "$install_root/current" || test -L "$install_root/current"; then
	test -L "$install_root/current" || { printf '%s\n' 'install-release: current is not a managed symlink' >&2; exit 1; }
	old_target=$(readlink "$install_root/current")
	case "$old_target" in
		releases/*)
			old_version=${old_target#releases/}
			case "$old_version" in ""|.*|*..*|*/*|*[!0-9A-Za-z._+-]*) printf '%s\n' 'install-release: current target is outside managed releases' >&2; exit 1 ;; esac
			;;
		*) printf '%s\n' 'install-release: current target is outside managed releases' >&2; exit 1 ;;
	esac
	ln -s "$old_target" "$previous_next"
	replace_link "$previous_next" "$install_root/previous"
fi
ln -s "releases/$version" "$current_next"
replace_link "$current_next" "$install_root/current"
sync
if test "$selected_mode" = true; then
	test ! -e "$operation_receipt" && test ! -L "$operation_receipt" || { printf '%s\n' 'install-release: operation receipt appeared concurrently' >&2; exit 1; }
	mv -- "$operation_pending" "$operation_receipt"
	sync
fi
cleanup
trap - EXIT HUP INT TERM
printf '%s\n' "Bria $version installed; service was not started"
