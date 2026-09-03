#!/bin/sh
set -eu

role=${BRIA_ROLE:-combined}
config=${BRIA_CONFIG:-/etc/bria/config.json}
provider_lock=${BRIA_PROVIDER_LOCK:-/etc/bria/provider-lock.json}

while test "$#" -gt 0; do
	case "$1" in
		--role)
			test "$#" -ge 2 || { printf '%s\n' 'bria-container: --role requires a value' >&2; exit 2; }
			role=$2
			shift 2
			;;
		--config)
			test "$#" -ge 2 || { printf '%s\n' 'bria-container: --config requires a value' >&2; exit 2; }
			config=$2
			shift 2
			;;
		--provider-lock)
			test "$#" -ge 2 || { printf '%s\n' 'bria-container: --provider-lock requires a value' >&2; exit 2; }
			provider_lock=$2
			shift 2
			;;
		*)
			printf '%s\n' "bria-container: unsupported argument: $1" >&2
			exit 2
			;;
	esac
done

case "$role" in
	combined|coordinator|executor) ;;
	*)
		printf '%s\n' "bria-container: invalid role: $role" >&2
		exit 2
		;;
esac

case "$config" in
	/*) ;;
	*) printf '%s\n' 'bria-container: configuration path must be absolute' >&2; exit 2 ;;
esac
test -f "$config" || { printf '%s\n' "bria-container: configuration is not mounted: $config" >&2; exit 78; }
case "$provider_lock" in
	/*) ;;
	*) printf '%s\n' 'bria-container: provider lock path must be absolute' >&2; exit 2 ;;
esac
test -f "$provider_lock" || { printf '%s\n' "bria-container: provider lock is not mounted: $provider_lock" >&2; exit 78; }

/opt/bria/bria-container-preflight \
	--config "$config" \
	--lock "$provider_lock" \
	--role "$role"
/opt/bria/bria check-config --config "$config"

exec /opt/bria/bria run --config "$config"
