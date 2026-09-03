#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

fail() {
	printf '%s\n' "operational contract: $*" >&2
	exit 1
}

require_file() {
	test -f "$repo_dir/$1" || fail "missing $1"
}

require_executable() {
	test -x "$repo_dir/$1" || fail "$1 is not executable"
}

require_file Dockerfile
require_file docker/entrypoint.sh
require_file docker/compose.example.yaml
require_file packaging/macos/com.time4mind.bria.plist.tmpl
require_file packaging/linux/bria.service.tmpl
require_file packaging/wsl/bria.service.tmpl
require_executable packaging/render-service.sh
require_executable packaging/validate-install.sh
require_executable packaging/build-local.sh
require_executable packaging/build-release.sh
require_executable packaging/verify-release.sh
require_executable packaging/install-release.sh
require_executable packaging/rollback-install.sh
require_executable packaging/service-control.sh
require_executable packaging/postflight.sh
require_executable packaging/verify-supply-chain.sh
require_file .github/workflows/platform.yml
require_file .github/workflows/release.yml
require_file packaging/releasemanifest/main.go

grep -q '^USER [^0]' "$repo_dir/Dockerfile" || fail "Dockerfile must select a non-root user"
grep -q 'main.version=' "$repo_dir/Dockerfile" || fail "Dockerfile must inject a release version"
grep -q 'org.opencontainers.image.revision' "$repo_dir/Dockerfile" || fail "Docker image must identify its source revision"
grep -q '^ENV HOME=/var/lib/bria/provider-auth' "$repo_dir/Dockerfile" || fail "Docker provider auth must use persistent storage"
grep -q 'BRIA_ROLE' "$repo_dir/docker/entrypoint.sh" || fail "Docker entrypoint must validate a role"
grep -q 'bria-container-preflight' "$repo_dir/Dockerfile" || fail "Docker image must contain the provider-lock preflight"
grep -q 'BRIA_PROVIDER_LOCK' "$repo_dir/docker/entrypoint.sh" || fail "Docker entrypoint must require a provider lock"
grep -q '/workspaces' "$repo_dir/docker/compose.example.yaml" || fail "Docker example must expose explicit workspace mounts"
grep -q 'BRIA_UID' "$repo_dir/docker/compose.example.yaml" || fail "Docker example must support bind-mount owner identity"
for role in combined coordinator executor; do
	grep -q "^  $role:" "$repo_dir/docker/compose.example.yaml" || fail "Docker example lacks $role service"
done
if grep -Eiq '(token|password|secret)[[:space:]]*[:=][[:space:]]*[^${@<[:space:]]+' "$repo_dir/docker/compose.example.yaml"; then
	fail "Docker example appears to embed a secret"
fi

for template in \
	packaging/macos/com.time4mind.bria.plist.tmpl \
	packaging/linux/bria.service.tmpl \
	packaging/wsl/bria.service.tmpl
do
	grep -q '@BRIA_BIN@' "$repo_dir/$template" || fail "$template lacks binary placeholder"
	grep -q '@BRIA_CONFIG@' "$repo_dir/$template" || fail "$template lacks config placeholder"
done
grep -q '<string>/dev/null</string>' "$repo_dir/packaging/macos/com.time4mind.bria.plist.tmpl" || fail "launchd raw stdout/stderr must not be persisted"
for template in packaging/linux/bria.service.tmpl packaging/wsl/bria.service.tmpl; do
	grep -q '^StandardOutput=null$' "$repo_dir/$template" || fail "$template must discard raw stdout"
	grep -q '^StandardError=null$' "$repo_dir/$template" || fail "$template must discard raw stderr"
done
grep -q 'detailedRetention = 6 \* time.Hour' "$repo_dir/internal/safelog/logger.go" || fail "detailed safelog retention changed"
grep -q 'serviceRetention  = 24 \* time.Hour' "$repo_dir/internal/safelog/logger.go" || fail "service safelog retention changed"
grep -q 'criticalRetention = 72 \* time.Hour' "$repo_dir/internal/safelog/logger.go" || fail "critical safelog retention changed"

for script in \
	docker/entrypoint.sh \
	packaging/render-service.sh \
	packaging/validate-install.sh \
	packaging/build-local.sh \
	packaging/build-release.sh \
	packaging/verify-release.sh \
	packaging/install-release.sh \
	packaging/rollback-install.sh \
	packaging/service-control.sh \
	packaging/postflight.sh \
	packaging/verify-supply-chain.sh
do
	sh -n "$repo_dir/$script" || fail "$script has invalid shell syntax"
done

temporary=$(mktemp -d "${TMPDIR:-/tmp}/bria-ops-test.XXXXXX")
trap 'rm -rf -- "$temporary"' EXIT HUP INT TERM
for platform in macos linux wsl; do
	output=$temporary/$platform.service
	"$repo_dir/packaging/render-service.sh" "$platform" \
		/opt/bria/bria /etc/bria/config.json /var/log/bria "$output"
	grep -q '/opt/bria/bria' "$output" || fail "$platform renderer lost the binary path"
	grep -q '/etc/bria/config.json' "$output" || fail "$platform renderer lost the config path"
done
set +e
"$repo_dir/packaging/render-service.sh" macos \
	'/unsafe path/bria' /etc/bria/config.json /var/log/bria "$temporary/unsafe.service" >/dev/null 2>&1
unsafe_path_status=$?
set -e
test "$unsafe_path_status" -eq 2 || fail "service renderer must reject unsafe unescaped paths"

set +e
BRIA_ROLE=invalid "$repo_dir/docker/entrypoint.sh" >/dev/null 2>&1
invalid_role_status=$?
BRIA_ROLE=coordinator "$repo_dir/docker/entrypoint.sh" >/dev/null 2>&1
coordinator_missing_mount_status=$?
set -e
test "$invalid_role_status" -eq 2 || fail "invalid Docker role must exit 2"
test "$coordinator_missing_mount_status" -eq 78 || fail "coordinator must accept its role and fail closed on missing mounts"

grep -q 'darwin/amd64 darwin/arm64 linux/amd64 linux/arm64' "$repo_dir/packaging/build-release.sh" || fail "release target matrix is incomplete"
grep -q 'sole release identity and integrity authority' "$repo_dir/packaging/verify-release.sh" || fail "signed manifest is not authoritative"
grep -q 'releasemanifest sign' "$repo_dir/packaging/build-release.sh" || fail "release signing is not wired"
grep -q 'releasemanifest verify' "$repo_dir/packaging/verify-release.sh" || fail "signed release verification is not wired"
if grep -q -- '-private-key-file' "$repo_dir/packaging/build-release.sh" "$repo_dir/.github/workflows/release.yml"; then
	fail "release signing key must not be passed in process arguments"
fi
grep -q 'RELEASE_SIGNING_KEY_PEM.*secrets.RELEASE_SIGNING_KEY_PEM' "$repo_dir/.github/workflows/release.yml" || fail "release workflow signing secret is not explicit"
grep -q 'Remove ephemeral signing key' "$repo_dir/.github/workflows/release.yml" || fail "release workflow does not remove ephemeral key material"
grep -q 'macos-latest' "$repo_dir/.github/workflows/platform.yml" || fail "macOS CI job is missing"
grep -q 'wsl-contract' "$repo_dir/.github/workflows/platform.yml" || fail "WSL evidence boundary job is missing"
grep -q 'docker-build' "$repo_dir/.github/workflows/platform.yml" || fail "Docker build CI job is missing"
grep -q 'make check-full' "$repo_dir/.github/workflows/release.yml" || fail "release upload must depend on the complete repository gate"
grep -q './scripts release-evidence' "$repo_dir/Makefile" || fail "release target does not enforce external evidence"
grep -q 'actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c' "$repo_dir/.github/workflows/release.yml" || fail "release workflow does not import external evidence through the reviewed action"
grep -q 'run-id:.*inputs.evidence_run_id' "$repo_dir/.github/workflows/release.yml" || fail "release workflow does not identify the evidence-producing run"
grep -q 'BRIA_RELEASE_EVIDENCE_MANIFEST=' "$repo_dir/.github/workflows/release.yml" || fail "release workflow does not bind the evidence manifest"
grep -q 'packaging/verify-supply-chain.sh' "$repo_dir/Makefile" || fail "release target does not enforce immutable dependencies"

supply_fixture=$temporary/supply-chain
mkdir -p "$supply_fixture/.github/workflows"
printf '%s\n' \
	'ARG GO_IMAGE=golang:1.22.12-alpine3.21' \
	'ARG RUNTIME_IMAGE=alpine:3.21' >"$supply_fixture/Dockerfile"
printf '%s\n' 'steps:' '  - uses: actions/checkout@v4' >"$supply_fixture/.github/workflows/check.yml"
set +e
"$repo_dir/packaging/verify-supply-chain.sh" "$supply_fixture" >/dev/null 2>&1
mutable_supply_status=$?
set -e
test "$mutable_supply_status" -ne 0 || fail "mutable supply-chain references were accepted"
digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
revision=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
printf 'ARG GO_IMAGE=golang:1.22.12-alpine3.21@sha256:%s\nARG RUNTIME_IMAGE=alpine:3.21@sha256:%s\n' "$digest" "$digest" >"$supply_fixture/Dockerfile"
printf 'steps:\n  - uses: actions/checkout@%s # v4.2.2\n' "$revision" >"$supply_fixture/.github/workflows/check.yml"
"$repo_dir/packaging/verify-supply-chain.sh" "$supply_fixture" >/dev/null || fail "immutable supply-chain fixture was rejected"

for platform in macos linux wsl; do
	case "$platform" in macos) fixture_os=darwin ;; *) fixture_os=linux ;; esac
	fixture_version=0.0.0-contract-$platform
	fixture_root=$temporary/install-$platform
	fixture_release=$temporary/$fixture_version
	fixture_bundle=$temporary/fixture-$platform/bria_${fixture_version}_${fixture_os}_amd64
	mkdir -p "$fixture_bundle" "$fixture_root" "$fixture_release"
	for binary in bria bria-codex-adapter bria-claude-adapter bria-releasemanifest; do
		printf '#!/bin/sh\nif test "$1" = --version; then printf "bria %s\\n" "%s"; fi\n' "$fixture_version" "$fixture_version" >"$fixture_bundle/$binary"
		chmod 0755 "$fixture_bundle/$binary"
	done
	printf '{"version":"%s","revision":"contract","os":"%s","arch":"amd64","source_date_epoch":0}\n' "$fixture_version" "$fixture_os" >"$fixture_bundle/release.json"
	cp "$repo_dir/packaging/render-service.sh" "$repo_dir/packaging/validate-install.sh" \
		"$repo_dir/packaging/install-release.sh" "$repo_dir/packaging/rollback-install.sh" \
		"$repo_dir/packaging/service-control.sh" "$repo_dir/packaging/postflight.sh" "$fixture_bundle/"
	if test "$platform" = macos; then
		cp "$repo_dir/packaging/macos/com.time4mind.bria.plist.tmpl" "$fixture_bundle/"
	else
		cp "$repo_dir/packaging/linux/bria.service.tmpl" "$fixture_bundle/bria.service.tmpl"
		cp "$repo_dir/packaging/wsl/bria.service.tmpl" "$fixture_bundle/bria-wsl.service.tmpl"
	fi
	tar -czf "$fixture_release/bria_${fixture_version}_${fixture_os}_amd64.tar.gz" -C "$(dirname -- "$fixture_bundle")" "$(basename -- "$fixture_bundle")"
	# The installer must reject an unsigned fixture before changing current.
	set +e
	"$repo_dir/packaging/install-release.sh" "$platform" amd64 "$fixture_release" /nonexistent/trust.json "$fixture_root" /absolute/config.json >/dev/null 2>&1
	unsigned_status=$?
	set -e
	test "$unsigned_status" -ne 0 || fail "installer accepted unsigned $platform fixture"
	test ! -e "$fixture_root/current" || fail "failed install changed current for $platform"
done

printf '%s\n' 'Operational packaging contracts: OK'
