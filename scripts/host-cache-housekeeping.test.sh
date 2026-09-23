#!/usr/bin/env bash
# Behavioral tests for host-cache-housekeeping.sh.

set -Eeuo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root_dir/scripts/host-cache-housekeeping.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*"
  exit 1
}

assert_file() {
  [[ -e "$1" ]] || fail "expected file: $1"
}

assert_missing() {
  [[ ! -e "$1" ]] || fail "expected missing path: $1"
}

fake_bin="$tmp_dir/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/df" <<'EOF'
#!/usr/bin/env bash
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf 'fake 100 90 10 %s%% /\n' "${FAKE_USED_PERCENT:-90}"
EOF
chmod +x "$fake_bin/df"
cat >"$fake_bin/multica" <<'EOF'
#!/usr/bin/env bash
if [[ "${FAKE_STATUS_MODE:-idle}" == fail ]]; then
  exit 1
fi
if [[ -n "${FAKE_STATUS_JSON:-}" ]]; then
  printf '%s\n' "$FAKE_STATUS_JSON"
else
  printf '%s\n' '{"active_task_count":0,"running_task_count":0,"resource_wait_task_count":0}'
fi
EOF
chmod +x "$fake_bin/multica"

run_housekeeping() {
  local mode="$1"
  PATH="$fake_bin:$PATH" \
    HOME="$tmp_dir/home" \
    HOST_CACHE_LOG="$tmp_dir/housekeeping.log" \
    HOST_CACHE_LOCK_FILE="$tmp_dir/housekeeping.lock" \
    FAKE_USED_PERCENT="${FAKE_USED_PERCENT:-90}" \
    FAKE_STATUS_MODE="${FAKE_STATUS_MODE:-idle}" \
    FAKE_STATUS_JSON="${FAKE_STATUS_JSON:-}" \
    "$script" "$mode" >/dev/null
}

mkdir -p "$tmp_dir/home/.cache/go-build" "$tmp_dir/home/.cache/yarn"
printf go >"$tmp_dir/home/.cache/go-build/marker"
printf yarn >"$tmp_dir/home/.cache/yarn/marker"

# Below the high-water mark: no status call and no mutation.
FAKE_USED_PERCENT=73 run_housekeeping --apply
assert_file "$tmp_dir/home/.cache/go-build/marker"
assert_file "$tmp_dir/home/.cache/yarn/marker"

# An active task blocks all mutation.
FAKE_USED_PERCENT=90 \
FAKE_STATUS_JSON='{"active_task_count":1,"running_task_count":1,"resource_wait_task_count":0}' \
run_housekeeping --apply
assert_file "$tmp_dir/home/.cache/go-build/marker"
assert_file "$tmp_dir/home/.cache/yarn/marker"

# An unavailable daemon also fails closed.
FAKE_USED_PERCENT=90 FAKE_STATUS_MODE=fail run_housekeeping --apply
assert_file "$tmp_dir/home/.cache/go-build/marker"

# Dry-run reports but leaves cache contents intact.
FAKE_USED_PERCENT=90 FAKE_STATUS_MODE=idle run_housekeeping --dry-run
assert_file "$tmp_dir/home/.cache/go-build/marker"
assert_file "$tmp_dir/home/.cache/yarn/marker"

# With no active task, apply clears only the two exact cache roots.
FAKE_USED_PERCENT=90 FAKE_STATUS_MODE=idle run_housekeeping --apply
assert_missing "$tmp_dir/home/.cache/go-build/marker"
assert_missing "$tmp_dir/home/.cache/yarn/marker"

# A stale editor server absent from the LRU list is reclaimable.
stale_server="$tmp_dir/home/.vscode-server-insiders/cli/servers/Insiders-stale"
mkdir -p "$stale_server/server"
printf stale >"$stale_server/server/marker"
printf '[]\n' >"$tmp_dir/home/.vscode-server-insiders/cli/servers/lru.json"
touch -d '30 days ago' "$stale_server"
FAKE_USED_PERCENT=90 FAKE_STATUS_MODE=idle run_housekeeping --apply
assert_missing "$stale_server"

grep -Fq 'no active or queued task' "$tmp_dir/housekeeping.log" \
  || fail 'expected quiescent-run log'
printf 'host-cache-housekeeping tests passed\n'
