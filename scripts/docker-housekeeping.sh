#!/usr/bin/env bash
# Keep Docker cleanup policy in one place for every local project on this host.
#
# This deliberately removes only dangling images and unused BuildKit cache.
# It never removes tagged images, containers, or volumes.
set -Eeuo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
MAX_USED_SPACE="${DOCKER_BUILDER_MAX_USED_SPACE:-4GB}"
MIN_FREE_SPACE="${DOCKER_BUILDER_MIN_FREE_SPACE:-12GB}"
LOCK_FILE="${DOCKER_BUILD_LOCK_FILE:-/tmp/multica-docker-build.lock}"
LOCK_TIMEOUT="${DOCKER_BUILD_LOCK_TIMEOUT:-900}"

to_bytes() {
  local value="${1^^}"
  local normalized="${value//B/}"
  local parsed

  [[ -n "$normalized" ]] || return 1
  if command -v numfmt >/dev/null 2>&1; then
    parsed="$(numfmt --from=auto "$normalized" 2>/dev/null || true)"
    parsed="${parsed//[[:space:]]/}"
    [[ "$parsed" =~ ^[0-9]+$ ]] && {
      printf '%s\n' "$parsed"
      return 0
    }
  fi

  if [[ "$normalized" =~ ^([0-9]+([.][0-9]+)?)([KMGTPE]I?)?$ ]]; then
    local number="${BASH_REMATCH[1]}"
    local suffix="${BASH_REMATCH[3]:-}"
    local multiplier=1
    case "$suffix" in
      K) multiplier=1000 ;;
      M) multiplier=1000000 ;;
      G) multiplier=1000000000 ;;
      T) multiplier=1000000000000 ;;
      P) multiplier=1000000000000000 ;;
      E) multiplier=1000000000000000000 ;;
      KI) multiplier=1024 ;;
      MI) multiplier=1048576 ;;
      GI) multiplier=1073741824 ;;
      TI) multiplier=1099511627776 ;;
      PI) multiplier=1125899906842624 ;;
      EI) multiplier=1152921504606846976 ;;
    esac
    awk -v number="$number" -v multiplier="$multiplier" \
      'BEGIN { printf "%.0f\n", number * multiplier }'
    return 0
  fi
  return 1
}

filesystem_free_bytes() {
  df -Pk "$SCRIPT_DIR" 2>/dev/null \
    | awk 'NR == 2 { printf "%.0f\n", $4 * 1024; exit }'
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  cat <<'USAGE'
Usage: docker-housekeeping.sh

Environment:
  DOCKER_BUILDER_MAX_USED_SPACE
                              Maximum BuildKit cache to retain (default: 4GB)
  DOCKER_BUILDER_MIN_FREE_SPACE
                              Desired free space after pruning (default: 12GB;
                              set empty to disable when supported)
  DOCKER_BUILD_LOCK_FILE       Shared build/cleanup lock (default: /tmp/multica-docker-build.lock)
  DOCKER_BUILD_LOCK_TIMEOUT    Lock wait timeout in seconds (default: 900)
USAGE
  exit 0
fi

command -v docker >/dev/null 2>&1 || {
  printf '[docker-housekeeping] docker is required\n' >&2
  exit 1
}
command -v flock >/dev/null 2>&1 || {
  printf '[docker-housekeeping] flock is required\n' >&2
  exit 1
}

mkdir -p -- "$(dirname -- "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
if ! flock -w "$LOCK_TIMEOUT" 9; then
  printf '[docker-housekeeping] timed out waiting for %s\n' "$LOCK_FILE" >&2
  exit 1
fi

printf '[docker-housekeeping] removing dangling images\n'
if ! docker image prune -f; then
  printf '[docker-housekeeping] image cleanup failed; continuing\n' >&2
fi

prune_help="$(docker builder prune --help 2>&1 || true)"
prune_args=(--all --force)
if grep -q -- '--max-used-space' <<<"$prune_help"; then
  # Current Docker/buildx uses --max-used-space. The old --keep-storage name
  # is retained as a compatibility fallback for older Docker installations.
  prune_args+=(--max-used-space "$MAX_USED_SPACE")
elif grep -q -- '--keep-storage' <<<"$prune_help"; then
  prune_args+=(--keep-storage "$MAX_USED_SPACE")
else
  printf '[docker-housekeeping] Docker CLI has no supported cache-size limit; skipping BuildKit cleanup\n' >&2
  exit 1
fi
if [[ -n "$MIN_FREE_SPACE" ]] && grep -q -- '--min-free-space' <<<"$prune_help"; then
  prune_args+=(--min-free-space "$MIN_FREE_SPACE")
fi

printf '[docker-housekeeping] pruning unused BuildKit cache (max %s, target free %s)\n' \
  "$MAX_USED_SPACE" "${MIN_FREE_SPACE:-disabled}"
if ! docker builder prune "${prune_args[@]}"; then
  printf '[docker-housekeeping] BuildKit cleanup failed; continuing\n' >&2
fi

# Some Docker versions accept --min-free-space but still retain the cache when
# --max-used-space is below the private cache size. Check the postcondition
# ourselves and fall back to an unrestricted cache prune only when the host is
# still below the requested free-space floor. This preserves warm cache during
# normal operation while preventing the floor from becoming advisory exactly
# when the disk is nearly full.
if [[ -n "$MIN_FREE_SPACE" ]]; then
  min_free_bytes="$(to_bytes "$MIN_FREE_SPACE" || true)"
  free_bytes="$(filesystem_free_bytes || true)"
  if [[ "$min_free_bytes" =~ ^[0-9]+$ ]] \
    && [[ "$free_bytes" =~ ^[0-9]+$ ]] \
    && (( free_bytes < min_free_bytes )); then
    printf '[docker-housekeeping] free space remains below %s; reclaiming all unused BuildKit cache\n' \
      "$MIN_FREE_SPACE"
    if ! docker builder prune --all --force; then
      printf '[docker-housekeeping] emergency BuildKit cleanup failed; continuing\n' >&2
    fi
  fi
fi
