#!/bin/sh

# Atomically replace a macOS activation symlink. BSD mv follows a destination
# symlink to a directory unless -h is present, which would leave current on the
# old release and move the temporary link inside it.
macos_atomic_symlink() {
  target=$1
  link=$2
  temporary=$3
  case "$target$link$temporary" in *"
"*) return 2 ;; esac
  if [ -e "$link" ] && [ ! -L "$link" ]; then
    return 2
  fi
  rm -f "$temporary"
  ln -s "$target" "$temporary"
  /bin/mv -f -h "$temporary" "$link"
}

macos_acquire_install_lock() {
  lock_path=$1
  owner_start=$(/bin/ps -p "$$" -o lstart= 2>/dev/null | sed -e 's/^ *//' -e 's/ *$//')
  [ -n "$owner_start" ] || return 1
  macos_install_owner_token="$$|$owner_start"
  if mkdir "$lock_path" 2>/dev/null; then
    printf '%s\n' "$macos_install_owner_token" >"$lock_path/owner"
    return 0
  fi
  owner_record=""
  [ ! -r "$lock_path/owner" ] || owner_record=$(sed -n '1p' "$lock_path/owner")
  owner=${owner_record%%|*}
  lock_mtime=$(/usr/bin/stat -f '%m' "$lock_path" 2>/dev/null || printf 0)
  lock_now=$(date +%s)
  lock_age=$((lock_now - lock_mtime))
  case "$owner" in
    ''|*[!0-9]*) [ "$lock_age" -ge 600 ] || return 1 ;;
    *)
      if kill -0 "$owner" 2>/dev/null; then
        case "$owner_record" in
          *'|'*)
            live_start=$(/bin/ps -p "$owner" -o lstart= 2>/dev/null | sed -e 's/^ *//' -e 's/ *$//')
            [ -n "$live_start" ] || return 1
            [ "$owner_record" != "$owner|$live_start" ] || return 1
            ;;
          *) return 1 ;;
        esac
      fi
      ;;
  esac
  stale="$lock_path.stale.$$"
  /bin/mv -f -h "$lock_path" "$stale" 2>/dev/null || return 1
  rm -f "$stale/owner"
  rmdir "$stale" 2>/dev/null || return 1
  mkdir "$lock_path" 2>/dev/null || return 1
  printf '%s\n' "$macos_install_owner_token" >"$lock_path/owner"
}

macos_release_install_lock() {
  lock_path=$1
  [ -r "$lock_path/owner" ] || return 0
  [ "$(sed -n '1p' "$lock_path/owner")" = "${macos_install_owner_token:-}" ] || return 0
  rm -f "$lock_path/owner"
  rmdir "$lock_path" 2>/dev/null || true
}
