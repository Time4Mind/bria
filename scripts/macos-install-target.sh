#!/bin/sh

# Resolve the node profile without silently changing an installed LaunchAgent.
# Explicit environment values always win. A fresh install uses the ordinary
# ~/.bria bootstrap path, whose cluster init flow starts with one local voter.
macos_install_target_error() {
  printf 'cannot preserve installed Bria profile: %s\n' "$1" >&2
  return 1
}

resolve_macos_install_target() {
  resolved_data_dir="${BRIA_DATA_DIR:-$HOME/.bria}"
  resolved_config="${BRIA_CONFIG:-$resolved_data_dir/config.json}"

  if [ "${BRIA_DATA_DIR+x}" = x ] || [ "${BRIA_CONFIG+x}" = x ]; then
		return 0
  fi

  installed_plist="$HOME/Library/LaunchAgents/com.time4mind.bria.plist"
	[ -e "$installed_plist" ] || return 0
	[ -r "$installed_plist" ] || {
		macos_install_target_error "LaunchAgent is not readable: $installed_plist"
		return 1
	}
  plist_buddy="${BRIA_PLIST_BUDDY:-/usr/libexec/PlistBuddy}"
	[ -x "$plist_buddy" ] || {
		macos_install_target_error "PlistBuddy is not executable: $plist_buddy"
		return 1
	}

  installed_arg1=$(
    "$plist_buddy" -c 'Print :ProgramArguments:1' "$installed_plist" 2>/dev/null
	) || {
		macos_install_target_error "missing ProgramArguments[1]"
		return 1
	}
  if [ "$installed_arg1" = node ]; then
    installed_node=$installed_arg1
    installed_run=$("$plist_buddy" -c 'Print :ProgramArguments:2' "$installed_plist" 2>/dev/null) || return 1
    installed_flag=$("$plist_buddy" -c 'Print :ProgramArguments:3' "$installed_plist" 2>/dev/null) || return 1
    installed_config=$("$plist_buddy" -c 'Print :ProgramArguments:4' "$installed_plist" 2>/dev/null) || return 1
  else
    installed_node=$("$plist_buddy" -c 'Print :ProgramArguments:2' "$installed_plist" 2>/dev/null) || return 1
    installed_run=$("$plist_buddy" -c 'Print :ProgramArguments:3' "$installed_plist" 2>/dev/null) || return 1
    installed_flag=$("$plist_buddy" -c 'Print :ProgramArguments:4' "$installed_plist" 2>/dev/null) || return 1
    installed_config=$("$plist_buddy" -c 'Print :ProgramArguments:5' "$installed_plist" 2>/dev/null) || return 1
  fi
  installed_data_dir=$(
    "$plist_buddy" -c 'Print :WorkingDirectory' "$installed_plist" 2>/dev/null
	) || {
		macos_install_target_error "missing WorkingDirectory"
		return 1
	}

	[ "$installed_node" = node ] || {
		macos_install_target_error "unexpected installed node command"
		return 1
	}
	[ "$installed_run" = run ] || {
		macos_install_target_error "unexpected installed run command"
		return 1
	}
	[ "$installed_flag" = --config ] || {
		macos_install_target_error "unexpected installed config flag"
		return 1
	}
  case "$installed_config$installed_data_dir" in
		*"
"*) macos_install_target_error "installed paths contain a newline"; return 1 ;;
  esac
	case "$installed_config" in
		/*) ;;
		*) macos_install_target_error "installed config path is not absolute"; return 1 ;;
	esac
	case "$installed_data_dir" in
		/*) ;;
		*) macos_install_target_error "installed data path is not absolute"; return 1 ;;
	esac

  resolved_data_dir="$installed_data_dir"
  resolved_config="$installed_config"
}
