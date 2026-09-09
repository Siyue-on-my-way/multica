#!/usr/bin/env bash
# Contract test for the shared Docker cleanup wrapper. It never talks to the
# host Docker daemon; the fake CLI only records the arguments it receives.
set -Eeuo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
HOUSEKEEPING="$SCRIPT_DIR/docker-housekeeping.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "$tmp_dir"' EXIT

fake_bin="$tmp_dir/bin"
mkdir -p -- "$fake_bin"
cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >>"$DOCKER_LOG"
case "$*" in
  "image prune -f")
    exit 0
    ;;
  "builder prune --help")
    if [[ "${DOCKER_FAKE_CLI:-modern}" == legacy ]]; then
      printf '      --keep-storage bytes  Maximum cache to keep\n'
    else
      printf '      --max-used-space bytes  Maximum cache to keep\n'
    fi
    printf '      --min-free-space bytes  Target free space\n'
    exit 0
    ;;
  builder\ prune\ *)
    exit 0
    ;;
esac
printf 'unexpected docker invocation: %s\n' "$*" >&2
exit 1
EOF
chmod +x "$fake_bin/docker"

run_case() {
  local mode="$1"
  local log="$tmp_dir/$mode.log"
  : >"$log"
  PATH="$fake_bin:$PATH" \
    DOCKER_LOG="$log" \
    DOCKER_FAKE_CLI="$mode" \
    DOCKER_BUILD_LOCK_FILE="$tmp_dir/$mode.lock" \
    DOCKER_BUILDER_MAX_USED_SPACE=4GB \
    DOCKER_BUILDER_MIN_FREE_SPACE=12GB \
    "$HOUSEKEEPING" >/dev/null

  grep -Fqx 'image prune -f' "$log"
  if [[ "$mode" == modern ]]; then
    grep -Fqx 'builder prune --all --force --max-used-space 4GB --min-free-space 12GB' "$log"
  else
    grep -Fqx 'builder prune --all --force --keep-storage 4GB --min-free-space 12GB' "$log"
  fi
}

run_case modern
run_case legacy
printf 'docker-housekeeping tests passed\n'
