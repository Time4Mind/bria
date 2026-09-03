#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
go_command=${GO:-go}
go_cache=${GOCACHE:-$repo_dir/.cache/go-build}
go_mod_cache=${GOMODCACHE:-$repo_dir/.cache/go-mod}
trust_file=${RELEASE_TRUST_FILE:-}
case "$trust_file" in /*) ;; *) printf '%s\n' 'verify-release: RELEASE_TRUST_FILE must be absolute' >&2; exit 2 ;; esac

if test "$#" -eq 0; then
	version=${VERSION:-}
	dist_root=${DIST_DIR:-$repo_dir/dist}
	case "$version" in ""|dev|.*|*..*|*[!0-9A-Za-z._+-]*) printf '%s\n' 'verify-release: invalid VERSION' >&2; exit 2 ;; esac
	release_dir=$dist_root/$version
elif test "$#" -eq 1; then
	release_dir=$1
else
	printf '%s\n' 'usage: verify-release.sh [ABSOLUTE_RELEASE_DIR]' >&2
	exit 2
fi
case "$release_dir" in /*) ;; *) printf '%s\n' 'verify-release: release directory must be absolute' >&2; exit 2 ;; esac
version=${release_dir##*/}
case "$version" in ""|dev|.*|*..*|*[!0-9A-Za-z._+-]*) printf '%s\n' 'verify-release: release directory has an invalid version name' >&2; exit 2 ;; esac
# release-manifest.json is the sole release identity and integrity authority.
# SHA256SUMS may be present for human tooling, but never overrides or augments
# the owner signature checked here.
(
	cd "$repo_dir"
	env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$go_cache" GOMODCACHE="$go_mod_cache" \
		"$go_command" run -mod=readonly ./packaging/releasemanifest verify \
		-release-dir "$release_dir" -version "$version" -trust-file "$trust_file"
) || { printf '%s\n' 'verify-release: signed canonical manifest rejected' >&2; exit 1; }

expected_files="bria_${version}_darwin_amd64.tar.gz bria_${version}_darwin_arm64.tar.gz bria_${version}_linux_amd64.tar.gz bria_${version}_linux_arm64.tar.gz"
for filename in $expected_files; do
	test -f "$release_dir/$filename" || { printf '%s\n' "verify-release: signed manifest artifact is missing: $filename" >&2; exit 1; }
	bundle=${filename%.tar.gz}
	case "$filename" in
		bria_${version}_darwin_amd64.tar.gz) artifact_os=darwin; artifact_arch=amd64; service_members='com.time4mind.bria.plist.tmpl' ;;
		bria_${version}_darwin_arm64.tar.gz) artifact_os=darwin; artifact_arch=arm64; service_members='com.time4mind.bria.plist.tmpl' ;;
		bria_${version}_linux_amd64.tar.gz) artifact_os=linux; artifact_arch=amd64; service_members='bria.service.tmpl bria-wsl.service.tmpl' ;;
		bria_${version}_linux_arm64.tar.gz) artifact_os=linux; artifact_arch=arm64; service_members='bria.service.tmpl bria-wsl.service.tmpl' ;;
	esac
	listing=$(tar -tzf "$release_dir/$filename") || { printf '%s\n' "verify-release: unreadable archive $filename" >&2; exit 1; }
	expected_members="bria bria-codex-adapter bria-claude-adapter bria-releasemanifest release.json render-service.sh validate-install.sh install-release.sh rollback-install.sh service-control.sh postflight.sh $service_members"
	member_count=0
	for archive_member in $listing; do
		if test "$archive_member" = "$bundle/"; then
			continue
		fi
		case " $expected_members " in
			*" ${archive_member#"$bundle/"} "*) ;;
			*) printf '%s\n' "verify-release: $filename contains unexpected member $archive_member" >&2; exit 1 ;;
		esac
		member_count=$((member_count + 1))
	done
	for member in $expected_members; do
		printf '%s\n' "$listing" | grep -Fqx "$bundle/$member" || { printf '%s\n' "verify-release: $filename is missing $member" >&2; exit 1; }
	done
	set -- $expected_members
	test "$member_count" -eq "$#" || { printf '%s\n' "verify-release: $filename has duplicate or malformed members" >&2; exit 1; }
	metadata=$(tar -xOzf "$release_dir/$filename" "$bundle/release.json") || { printf '%s\n' "verify-release: cannot read metadata from $filename" >&2; exit 1; }
	printf '%s\n' "$metadata" | grep -Fq "\"version\":\"$version\"" || { printf '%s\n' "verify-release: metadata version mismatch in $filename" >&2; exit 1; }
	printf '%s\n' "$metadata" | grep -Eq '"revision":"[0-9A-Za-z._+-]+"' || { printf '%s\n' "verify-release: source revision is missing in $filename" >&2; exit 1; }
	printf '%s\n' "$metadata" | grep -Fq "\"os\":\"$artifact_os\"" || { printf '%s\n' "verify-release: metadata OS mismatch in $filename" >&2; exit 1; }
	printf '%s\n' "$metadata" | grep -Fq "\"arch\":\"$artifact_arch\"" || { printf '%s\n' "verify-release: metadata architecture mismatch in $filename" >&2; exit 1; }
done
set -- "$release_dir"/*.tar.gz
test "$#" -eq 4 || { printf '%s\n' 'verify-release: release directory contains unmanifested archives' >&2; exit 1; }
printf '%s\n' 'Bria signed release manifest (4 artifacts): OK'
