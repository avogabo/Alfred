#!/usr/bin/env bash
set -euo pipefail

CFG="${ALFRED_CONFIG:-/config/config.json}"
RCLONE_CFG="/tmp/rclone-alfred.conf"
ALF_PID=""

# Persist FileBot runtime state and license across container recreations.
export FILEBOT_HOME="${FILEBOT_HOME:-/config/filebot}"

init_filebot_license() {
  local lpath="/config/filebot/license.psm"
  mkdir -p "$FILEBOT_HOME" /config/filebot >/dev/null 2>&1 || true
  if [ -x /usr/local/bin/filebot ] && [ -f "$lpath" ]; then
    echo "[entrypoint] filebot: activating license from $lpath (home=$FILEBOT_HOME)"
    /usr/local/bin/filebot --license "$lpath" >/tmp/filebot-license.log 2>&1 || true
  fi
}

start_alfred() {
  /usr/local/bin/alfrededr -fuse=false -config "$CFG" &
  ALF_PID=$!
}

wait_alfred_live() {
  local i
  for i in $(seq 1 60); do
    if curl -fsS http://127.0.0.1:1516/live >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

cleanup_mountpoint() {
  local mpath="$1"

  # Stop old rclone daemons first.
  pkill -f "rclone mount alfredwebdav:" >/dev/null 2>&1 || true

  # Repeated lazy unmount attempts help clear "Transport endpoint is not connected".
  umount -l "$mpath" >/dev/null 2>&1 || true
  fusermount3 -uz "$mpath" >/dev/null 2>&1 || true
  sleep 0.2
  umount -l "$mpath" >/dev/null 2>&1 || true
  fusermount3 -uz "$mpath" >/dev/null 2>&1 || true

  # Ensure the path exists again after cleanup.
  if ! mkdir -p "$mpath" 2>/dev/null; then
    echo "[entrypoint] mountpoint cleanup retry for $mpath"
    umount -l "$mpath" >/dev/null 2>&1 || true
    fusermount3 -uz "$mpath" >/dev/null 2>&1 || true
    sleep 0.5
    mkdir -p "$mpath"
  fi
}

mount_webdav_if_enabled() {
  [ -f "$CFG" ] || return 0
  local enabled url user pass mpath
  enabled=$(jq -r '.webdav_mount.enabled // false' "$CFG" 2>/dev/null || echo "false")
  [ "$enabled" = "true" ] || return 0

  url=$(jq -r '.webdav_mount.url // ""' "$CFG")
  user=$(jq -r '.webdav_mount.user // ""' "$CFG")
  pass=$(jq -r '.webdav_mount.pass // ""' "$CFG")
  mpath=$(jq -r '.webdav_mount.mount_path // "/host/mount/library"' "$CFG")

  if [ -z "$url" ]; then
    echo "[entrypoint] webdav_mount.enabled=true but url empty; skipping mount"
    return 0
  fi
  if [[ "$mpath" != /host/* ]]; then
    echo "[entrypoint] mount_path must be under /host for host visibility (got: $mpath)"
    return 1
  fi

  cleanup_mountpoint "$mpath"

  {
    echo "[alfredwebdav]"
    echo "type = webdav"
    echo "url = $url"
    echo "vendor = other"
    [ -n "$user" ] && echo "user = $user"
    if [ -n "$pass" ]; then
      echo "pass = $(rclone obscure "$pass")"
    fi
  } > "$RCLONE_CFG"

  rclone --config "$RCLONE_CFG" mount alfredwebdav: "$mpath" \
    --allow-other \
    --allow-non-empty \
    --default-permissions \
    --umask 002 \
    --uid 99 \
    --gid 100 \
    --dir-cache-time 5s \
    --poll-interval 5s \
    --vfs-cache-mode full \
    --vfs-cache-max-size 150G \
    --vfs-cache-max-age 6h \
    --vfs-read-chunk-size 56M \
    --vfs-read-chunk-size-limit 2G \
    --vfs-read-ahead 8G \
    --buffer-size 0 \
    --vfs-cache-poll-interval 1m \
    --timeout 1m \
    --retries 3 \
    --low-level-retries 10 \
    --daemon

  sleep 1
  mountpoint -q "$mpath" || { echo "[entrypoint] rclone mount failed at $mpath"; return 1; }
  echo "[entrypoint] webdav mounted at $mpath from $url (host-visible via bind propagation)"
}

init_filebot_license
start_alfred
if ! wait_alfred_live; then
  echo "[entrypoint] Alfred failed to become healthy"
  wait "$ALF_PID"
  exit 1
fi

if ! mount_webdav_if_enabled; then
  kill "$ALF_PID" >/dev/null 2>&1 || true
  wait "$ALF_PID" || true
  exit 1
fi

wait "$ALF_PID"
