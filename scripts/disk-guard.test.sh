#!/usr/bin/env bash
# scripts/disk-guard.sh 的行为测试（SIY-149）：快照、阈值告警、构建暂停。
#
# 全程只 mock docker/docker-compose 等命令，真实脚本原样执行；
# 断言 disk-guard 与 restart.sh 在磁盘不足时暂停构建，且没有删除数据的路径。
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_contains() { # <file> <expected-substring> <label>
  grep -Fq "$2" "$1" || fail "$3: expected to contain '$2'
Observed:
$(sed 's/^/  /' "$1")"
}

assert_not_contains() { # <file> <forbidden-substring> <label>
  ! grep -Fq "$2" "$1" || fail "$3: must NOT contain '$2'"
}

assert_rc() { # <expected-rc> <label> <cmd...>
  local expected=$1 label=$2
  shift 2
  set +e
  "$@" >/dev/null 2>&1
  local rc=$?
  set -e
  [[ "$rc" == "$expected" ]] || fail "$label: expected rc=$expected, got rc=$rc"
}

# --- mock docker: 稳定的统计数字，让断言不依赖宿主机状态 -------------------
fake_bin="$tmp_dir/bin"
mkdir -p "$fake_bin"
cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
case "$1 $2" in
  "info -f") echo "/var/lib/docker" ;;
  "images -f") printf 'id1\nid2\nid3\n' ;;
  "system df")
    cat <<'TBL'
TYPE            TOTAL     ACTIVE    SIZE      RECLAIMABLE
Images          18        5         18.62GB   15.87GB (85%)
Containers      8         3         1.20GB    300MB (25%)
Local Volumes   12        6         9.50GB    2.10GB (22%)
Build Cache     120       0         12.24GB   3.42GB (28%)
TBL
    ;;
  *) exit 0 ;;
esac
EOF
cat > "$fake_bin/docker-compose" <<EOF
#!/usr/bin/env bash
echo "docker-compose \$*" >>"$tmp_dir/compose.calls"
exit 0
EOF
cat > "$fake_bin/multica" <<'EOF'
#!/usr/bin/env bash
echo "daemon: stopped"
exit 0
EOF
: > "$fake_bin/sudo"      # no-op，避免测试触碰真实 /usr/local/bin
: > "$fake_bin/go"        # no-op，[2.5] 的 daemon 二进制重建走成功分支
: > "$fake_bin/sleep"     # no-op，加速 restart.sh 的状态检查
chmod +x "$fake_bin"/*
export PATH="$fake_bin:$PATH"

guard="$root_dir/scripts/disk-guard.sh"
restart="$root_dir/restart.sh"

# --- 1. report: 构建日志里能看到 df / docker system df / 镜像数 / BuildKit 缓存 ---
out="$("$guard" report 构建前 2>&1)"
echo "$out" >"$tmp_dir/report.out"
assert_contains "$tmp_dir/report.out" "磁盘快照（构建前）" "report title"
assert_contains "$tmp_dir/report.out" "已用" "report df usage"
assert_contains "$tmp_dir/report.out" "镜像: 18 个（悬空 3 个）" "report image count"
assert_contains "$tmp_dir/report.out" "BuildKit 缓存: 12.24GB（可回收 3.42GB）" "report buildkit cache"
assert_contains "$tmp_dir/report.out" "Images          18" "report docker system df table"
# 每次快照落一行趋势记录
assert_contains "$root_dir/logs/disk-usage-history.log" "label=构建前" "usage log"
assert_contains "$root_dir/logs/disk-usage-history.log" "buildkit_cache=12.24GB" "usage log cache"

# --- 2. docker 失败时降级而不是崩溃 ---
FAKE_DOCKER_FAIL="$tmp_dir/docker-fail"
mkdir -p "$FAKE_DOCKER_FAIL"
printf '#!/usr/bin/env bash\nexit 1\n' >"$FAKE_DOCKER_FAIL/docker"
chmod +x "$FAKE_DOCKER_FAIL/docker"
out="$(PATH="$FAKE_DOCKER_FAIL:$fake_bin:$PATH" "$guard" report 2>&1)" \
  || fail "report should survive docker failures"
printf '%s\n' "$out" >"$tmp_dir/report-degraded.out"
assert_contains "$tmp_dir/report-degraded.out" "Docker 统计不可用" "degraded report"
assert_contains "$tmp_dir/report-degraded.out" "文件系统" "degraded report keeps df"

# --- 3. check 三档: OK / WARN(记录告警, 放行) / BLOCK(退出码 2) ---
assert_rc 0 "check below warn" env DISK_WARN_USED_PERCENT=99 DISK_BLOCK_USED_PERCENT=100 "$guard" check
assert_rc 0 "check at warn level still passes" \
  env DISK_WARN_USED_PERCENT=1 DISK_BLOCK_USED_PERCENT=100 "$guard" check
DISK_ALERT_LOG="$tmp_dir/alerts-warn.log" DISK_WARN_USED_PERCENT=1 DISK_BLOCK_USED_PERCENT=100 \
  "$guard" check 2>/dev/null || fail "warn-level check must exit 0"
assert_contains "$tmp_dir/alerts-warn.log" "[WARN]" "warn alert logged"
assert_contains "$tmp_dir/alerts-warn.log" "未自动清理任何数据" "warn alert states no deletion"
assert_rc 2 "check at block level" \
  env DISK_ALERT_LOG="$tmp_dir/alerts-block.log" DISK_WARN_USED_PERCENT=1 DISK_BLOCK_USED_PERCENT=1 \
    "$guard" check
assert_contains "$tmp_dir/alerts-block.log" "[BLOCK]" "block alert logged"

# --- 4. 只预警不删除: disk-guard.sh 里不允许出现任何清理类命令 ---
sweeps='prune|rmi|volume rm|rm -rf|rm -f |container rm'
if grep -Eq "$sweeps" "$guard"; then
  fail "disk-guard.sh must not contain cleanup commands, found:
$(grep -En "$sweeps" "$guard")"
fi

# --- 5. restart.sh 集成 ---
bash -n "$restart" || fail "restart.sh syntax"
: >"$tmp_dir/compose.calls"
DISK_WARN_USED_PERCENT=99 DISK_BLOCK_USED_PERCENT=100 "$restart" --build >/dev/null 2>&1 \
  || fail "restart.sh --build should complete when disk is fine"
assert_contains "$tmp_dir/compose.calls" "docker-compose build" "restart builds when disk is fine"

: >"$tmp_dir/compose.calls"
DISK_ALERT_LOG="$tmp_dir/alerts-restart.log" DISK_WARN_USED_PERCENT=1 DISK_BLOCK_USED_PERCENT=1 \
  "$restart" --build >"$tmp_dir/restart-block.out" 2>&1
assert_not_contains "$tmp_dir/compose.calls" "docker-compose build" "restart pauses build at block level"
assert_contains "$tmp_dir/compose.calls" "docker-compose down" "restart still brings services down"
assert_contains "$tmp_dir/compose.calls" "docker-compose up" "restart still brings services up"
assert_contains "$tmp_dir/restart-block.out" "非必要构建已暂停" "restart explains paused build"
assert_contains "$tmp_dir/alerts-restart.log" "[BLOCK]" "restart run logged the block alert"

: >"$tmp_dir/compose.calls"
DISK_WARN_USED_PERCENT=1 DISK_BLOCK_USED_PERCENT=1 "$restart" --build --force-build >/dev/null 2>&1 \
  || fail "--force-build should build despite the block level"
assert_contains "$tmp_dir/compose.calls" "docker-compose build" "--force-build overrides the pause"

# --- 6. 日常重启（无参数）不构建，但预警照常记录 ---
: >"$tmp_dir/compose.calls"
DISK_ALERT_LOG="$tmp_dir/alerts-daily.log" DISK_WARN_USED_PERCENT=1 DISK_BLOCK_USED_PERCENT=1 \
  "$restart" >/dev/null 2>&1
assert_not_contains "$tmp_dir/compose.calls" "docker-compose build" "daily restart never builds"
assert_contains "$tmp_dir/alerts-daily.log" "[BLOCK]" "daily restart still records the alert"

echo "disk-guard tests passed"
