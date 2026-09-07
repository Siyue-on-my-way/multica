#!/usr/bin/env bash
# Keep Docker cleanup policy in one place for every local project on this host.
#
# This deliberately removes only dangling images and unused BuildKit cache.
# It never removes tagged images, containers, or volumes.
set -Eeuo pipefail

KEEP_STORAGE="${DOCKER_BUILDER_KEEP_STORAGE:-8GB}"
LOCK_FILE="${DOCKER_BUILD_LOCK_FILE:-/tmp/multica-docker-build.lock}"
LOCK_TIMEOUT="${DOCKER_BUILD_LOCK_TIMEOUT:-900}"

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  cat <<'USAGE'
Usage: docker-housekeeping.sh

Environment:
  DOCKER_BUILDER_KEEP_STORAGE  BuildKit cache floor (default: 8GB)
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

printf '[docker-housekeeping] pruning unused BuildKit cache (keep %s)\n' "$KEEP_STORAGE"
if ! docker builder prune -af --keep-storage "$KEEP_STORAGE"; then
  printf '[docker-housekeeping] BuildKit cleanup failed; continuing\n' >&2
fi
