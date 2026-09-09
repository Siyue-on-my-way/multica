#!/usr/bin/env bash
# Keep Docker cleanup policy in one place for every local project on this host.
#
# This deliberately removes only dangling images and unused BuildKit cache.
# It never removes tagged images, containers, or volumes.
set -Eeuo pipefail

MAX_USED_SPACE="${DOCKER_BUILDER_MAX_USED_SPACE:-4GB}"
MIN_FREE_SPACE="${DOCKER_BUILDER_MIN_FREE_SPACE:-12GB}"
LOCK_FILE="${DOCKER_BUILD_LOCK_FILE:-/tmp/multica-docker-build.lock}"
LOCK_TIMEOUT="${DOCKER_BUILD_LOCK_TIMEOUT:-900}"

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
