#!/bin/sh

# Resolve the node profile without silently changing an installed LaunchAgent.
# Explicit environment values always win. A fresh install uses the ordinary
# ~/.bria bootstrap path, whose cluster init flow starts with one local voter.
resolve_macos_install_target() {
  resolved_data_dir="${BRIA_DATA_DIR:-$HOME/.bria}"
  resolved_config="${BRIA_CONFIG:-$resolved_data_dir/config.json}"

  if [ "${BRIA_DATA_DIR+x}" = x ] || [ "${BRIA_CONFIG+x}" = x ]; then
    return
  fi

  installed_plist="$HOME/Library/LaunchAgents/com.time4mind.bria.plist"
  [ -r "$installed_plist" ] || return
  plist_buddy="${BRIA_PLIST_BUDDY:-/usr/libexec/PlistBuddy}"
  [ -x "$plist_buddy" ] || return

  installed_node=$(
    "$plist_buddy" -c 'Print :ProgramArguments:1' "$installed_plist" 2>/dev/null
  ) || return
  installed_run=$(
    "$plist_buddy" -c 'Print :ProgramArguments:2' "$installed_plist" 2>/dev/null
  ) || return
  installed_flag=$(
    "$plist_buddy" -c 'Print :ProgramArguments:3' "$installed_plist" 2>/dev/null
  ) || return
  installed_config=$(
    "$plist_buddy" -c 'Print :ProgramArguments:4' "$installed_plist" 2>/dev/null
  ) || return
  installed_data_dir=$(
    "$plist_buddy" -c 'Print :WorkingDirectory' "$installed_plist" 2>/dev/null
  ) || return

  [ "$installed_node" = node ] || return
  [ "$installed_run" = run ] || return
  [ "$installed_flag" = --config ] || return
  case "$installed_config$installed_data_dir" in
    *"
"*) return ;;
  esac
  case "$installed_config" in /*) ;; *) return ;; esac
  case "$installed_data_dir" in /*) ;; *) return ;; esac

  resolved_data_dir="$installed_data_dir"
  resolved_config="$installed_config"
}
