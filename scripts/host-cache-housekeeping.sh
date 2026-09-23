#!/usr/bin/env bash
# High-water-mark cleanup for host-side, regenerable development caches.
#
# The script is intentionally conservative:
#   * it does nothing below the disk-use trigger;
#   * it refuses to mutate anything while Multica reports an active task;
#   * it only clears the two explicitly owned cache directories below;
#   * old editor server directories are removed only when they are old, absent
#     from the editor LRU list, and not referenced by a running process.
#
# Manual invocation defaults to --dry-run. The systemd service passes --apply.

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
HOME_DIR="${HOME:-/root}"
DISK_TARGET="${HOST_CACHE_DISK_TARGET:-/}"
TRIGGER_USED_PERCENT="${HOST_CACHE_TRIGGER_USED_PERCENT:-85}"
LOCK_FILE="${HOST_CACHE_LOCK_FILE:-/tmp/multica-host-cache-housekeeping.lock}"
LOG_FILE="${HOST_CACHE_LOG:-$REPO_ROOT/logs/host-cache-housekeeping.log}"
MULTICA_BIN="${MULTICA_BIN:-multica}"
STATUS_TIMEOUT_SECONDS="${HOST_CACHE_STATUS_TIMEOUT_SECONDS:-15}"
EDITOR_MIN_AGE_DAYS="${HOST_CACHE_EDITOR_MIN_AGE_DAYS:-14}"

GO_CACHE_DIR="$HOME_DIR/.cache/go-build"
YARN_CACHE_DIR="$HOME_DIR/.cache/yarn"
VSCODE_SERVERS_DIR="$HOME_DIR/.vscode-server-insiders/cli/servers"
VSCODE_LRU_FILE="$VSCODE_SERVERS_DIR/lru.json"
TRAE_BIN_DIR="$HOME_DIR/.trae-server/bin"

MODE="dry-run"
case "${1:-}" in
  "") ;;
  --dry-run) MODE="dry-run" ;;
  --apply) MODE="apply" ;;
  --help|-h)
    sed -n '2,18p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    cat <<'USAGE'

Usage: host-cache-housekeeping.sh [--dry-run|--apply]

Environment:
  HOST_CACHE_TRIGGER_USED_PERCENT   Disk-use trigger (default: 85)
  HOST_CACHE_EDITOR_MIN_AGE_DAYS    Old editor server age (default: 14)
  HOST_CACHE_STATUS_TIMEOUT_SECONDS Multica status timeout (default: 15)
USAGE
    exit 0
    ;;
  *)
    printf 'unknown argument: %s\n' "$1" >&2
    exit 2
    ;;
esac

mkdir -p -- "$(dirname -- "$LOG_FILE")"

log() {
  local line="[host-cache-housekeeping] $*"
  printf '%s\n' "$line"
  printf '%s\n' "$line" >>"$LOG_FILE"
}

size_of() {
  du -sh -- "$1" 2>/dev/null | awk '{print $1}' || printf '?'
}

disk_used_percent() {
  df -P -- "$DISK_TARGET" 2>/dev/null \
    | awk 'NR == 2 { gsub(/%/, "", $5); print $5; exit }'
}

if ! [[ "$TRIGGER_USED_PERCENT" =~ ^[0-9]+$ ]] || (( TRIGGER_USED_PERCENT > 100 )); then
  log "invalid HOST_CACHE_TRIGGER_USED_PERCENT=$TRIGGER_USED_PERCENT; fail-closed"
  exit 1
fi

used_percent="$(disk_used_percent || true)"
if ! [[ "$used_percent" =~ ^[0-9]+$ ]]; then
  log "could not read disk usage for $DISK_TARGET; skipping"
  exit 0
fi
if (( used_percent < TRIGGER_USED_PERCENT )); then
  log "disk usage ${used_percent}% is below trigger ${TRIGGER_USED_PERCENT}%; no cleanup"
  exit 0
fi

if ! command -v flock >/dev/null 2>&1; then
  log "flock is unavailable; skipping mutation"
  exit 0
fi
mkdir -p -- "$(dirname -- "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  log "another host-cache housekeeping run holds $LOCK_FILE; skipping"
  exit 0
fi

if ! command -v jq >/dev/null 2>&1; then
  log "jq is unavailable; cannot prove task quiescence, skipping"
  exit 0
fi
if ! command -v "$MULTICA_BIN" >/dev/null 2>&1; then
  log "$MULTICA_BIN is unavailable; cannot prove task quiescence, skipping"
  exit 0
fi

status_json=""
if command -v timeout >/dev/null 2>&1; then
  if ! status_json="$(timeout "$STATUS_TIMEOUT_SECONDS" "$MULTICA_BIN" daemon status --output json 2>/dev/null)"; then
    log "daemon status failed or timed out; no cache mutation"
    exit 0
  fi
else
  if ! status_json="$("$MULTICA_BIN" daemon status --output json 2>/dev/null)"; then
    log "daemon status failed; no cache mutation"
    exit 0
  fi
fi

status_shape='(
  (.active_task_count | type == "number") and
  (.running_task_count | type == "number") and
  (.resource_wait_task_count | type == "number")
)'
if ! jq -e "$status_shape" <<<"$status_json" >/dev/null 2>&1; then
  log "daemon status JSON is incomplete; no cache mutation"
  exit 0
fi

if ! jq -e '(.active_task_count == 0 and .running_task_count == 0 and .resource_wait_task_count == 0)' \
  <<<"$status_json" >/dev/null 2>&1; then
  active="$(jq -r '.active_task_count' <<<"$status_json")"
  running="$(jq -r '.running_task_count' <<<"$status_json")"
  waiting="$(jq -r '.resource_wait_task_count' <<<"$status_json")"
  log "Multica is not quiescent (active=$active running=$running waiting=$waiting); skipping"
  exit 0
fi
log "disk usage ${used_percent}% reached trigger; Multica reports no active or queued task"

remove_cache_contents() {
  local dir="$1"
  local label="$2"
  local child
  local -a children=()

  # These exact paths are the only cache roots this script is allowed to clear.
  case "$dir" in
    "$HOME_DIR/.cache/go-build"|"$HOME_DIR/.cache/yarn") ;;
    *)
      log "refusing unexpected cache path: $dir"
      return 1
      ;;
  esac
  [[ -d "$dir" ]] || {
    log "$label is absent: $dir"
    return 0
  }

  while IFS= read -r -d '' child; do
    children+=("$child")
  done < <(find "$dir" -mindepth 1 -maxdepth 1 -print0 2>/dev/null)

  if ((${#children[@]} == 0)); then
    log "$label is already empty: $dir"
    return 0
  fi
  log "$label $dir uses $(size_of "$dir"); entries=${#children[@]} mode=$MODE"
  if [[ "$MODE" == apply ]]; then
    for child in "${children[@]}"; do
      rm -rf -- "$child"
    done
    log "$label cleared: $dir"
  else
    log "dry-run would clear only the contents of: $dir"
  fi
}

process_cmdline_contains() {
  local needle="$1"
  local proc_dir pid cmdline
  for proc_dir in /proc/[0-9]*; do
    pid="${proc_dir##*/}"
    [[ "$pid" == "$$" ]] && continue
    [[ -r "$proc_dir/cmdline" ]] || continue
    cmdline="$(tr '\0' ' ' <"$proc_dir/cmdline" 2>/dev/null || true)"
    [[ "$cmdline" == *"$needle"* ]] && return 0
  done
  return 1
}

vscode_lru_contains() {
  local id="$1"
  [[ -f "$VSCODE_LRU_FILE" ]] || return 0
  jq -e --arg id "$id" 'type == "array" and (index($id) != null)' "$VSCODE_LRU_FILE" \
    >/dev/null 2>&1
}

remove_old_vscode_servers() {
  local dir id
  [[ -d "$VSCODE_SERVERS_DIR" ]] || return 0
  if [[ ! -f "$VSCODE_LRU_FILE" ]] || ! jq -e 'type == "array"' "$VSCODE_LRU_FILE" >/dev/null 2>&1; then
    log "VS Code server LRU is missing or invalid; protecting all server versions"
    return 0
  fi

  while IFS= read -r -d '' dir; do
    id="${dir##*/}"
    if process_cmdline_contains "$dir/server"; then
      log "protecting active VS Code server: $id"
    elif vscode_lru_contains "$id"; then
      log "protecting LRU VS Code server: $id"
    elif ! find "$dir" -maxdepth 0 -type d -mtime "+$EDITOR_MIN_AGE_DAYS" -print -quit \
      | grep -q .; then
      log "protecting recent VS Code server: $id"
    elif [[ "$MODE" == apply ]]; then
      log "removing stale VS Code server $id ($(size_of "$dir"))"
      rm -rf -- "$dir"
    else
      log "dry-run would remove stale VS Code server $id ($(size_of "$dir"))"
    fi
  done < <(find "$VSCODE_SERVERS_DIR" -mindepth 1 -maxdepth 1 -type d -name 'Insiders-*' -print0 2>/dev/null)
}

remove_old_trae_bins() {
  local dir newest
  [[ -d "$TRAE_BIN_DIR" ]] || return 0
  newest="$(find "$TRAE_BIN_DIR" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null \
    | sort -nr | sed -n '1s/^[^ ]* //p')"
  while IFS= read -r -d '' dir; do
    if [[ "$dir" == "$newest" ]] || process_cmdline_contains "$dir"; then
      log "protecting active/newest Trae server: ${dir##*/}"
    elif ! find "$dir" -maxdepth 0 -type d -mtime "+$EDITOR_MIN_AGE_DAYS" -print -quit \
      | grep -q .; then
      log "protecting recent Trae server: ${dir##*/}"
    elif [[ "$MODE" == apply ]]; then
      log "removing stale Trae server ${dir##*/} ($(size_of "$dir"))"
      rm -rf -- "$dir"
    else
      log "dry-run would remove stale Trae server ${dir##*/} ($(size_of "$dir"))"
    fi
  done < <(find "$TRAE_BIN_DIR" -mindepth 1 -maxdepth 1 -type d -name 'stable-*' -print0 2>/dev/null)
}

remove_cache_contents "$GO_CACHE_DIR" "Go build cache"
remove_cache_contents "$YARN_CACHE_DIR" "Yarn cache"
remove_old_vscode_servers
remove_old_trae_bins
log "housekeeping completed in $MODE mode"
