#!/usr/bin/env bash
# 磁盘监控与预警（SIY-149）：只观测、只告警，从不删除任何数据。
#
# 用法:
#   disk-guard.sh report [标签]   打印磁盘/Docker 快照（restart.sh 在构建前后调用）
#   disk-guard.sh check           阈值检查。退出码: 0 正常或告警, 2 空间不足应暂停构建
#
# restart.sh 的约定:
#   - 构建前后各打一次 report 进入构建日志，对比可见本次构建的磁盘消耗；
#   - check 退出码为 2 时暂停非必要构建（--force-build 可显式越过），
#     服务仍用现有镜像重启，且不触发任何清理。
#
# 告警与趋势记录（logs/ 已被 .gitignore 的 *.log 覆盖）:
#   DISK_ALERT_LOG  默认 <repo>/logs/disk-alerts.log        每条告警一行
#   DISK_USAGE_LOG  默认 <repo>/logs/disk-usage-history.log 每次快照一行，看增长趋势
#
# 可选周期巡检（按需自行注册 crontab，脚本不自动安装）:
#   */10 * * * * <repo>/scripts/disk-guard.sh check >> <repo>/logs/disk-guard-cron.log 2>&1

set -Eeuo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DISK_WARN_USED_PERCENT="${DISK_WARN_USED_PERCENT:-85}"
DISK_BLOCK_USED_PERCENT="${DISK_BLOCK_USED_PERCENT:-92}"
DISK_GUARD_TARGET="${DISK_GUARD_TARGET:-}"
DISK_ALERT_LOG="${DISK_ALERT_LOG:-$REPO_ROOT/logs/disk-alerts.log}"
DISK_USAGE_LOG="${DISK_USAGE_LOG:-$REPO_ROOT/logs/disk-usage-history.log}"

RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'; NC='\033[0m'

guard_target() {
  if [[ -n "$DISK_GUARD_TARGET" ]]; then
    printf '%s' "$DISK_GUARD_TARGET"
    return
  fi
  local root
  root="$(docker info -f '{{.DockerRootDir}}' 2>/dev/null || true)"
  if [[ -n "$root" && -d "$root" ]]; then
    printf '%s' "$root"
  else
    printf '%s' "/"
  fi
}

# df -Ph 第二行: Filesystem Size Used Avail Use% Mounted-on
df_stats() {
  df -Ph "$1" 2>/dev/null | awk 'NR==2 {print $1, $2, $3, $4, $5, $6}'
}

append_line() { # <file> <line>
  mkdir -p -- "$(dirname -- "$1")"
  printf '%s\n' "$2" >>"$1"
}

emit_alert() { # <level> <message>
  local level="$1" message="$2" color="$YELLOW"
  if [[ "$level" == "BLOCK" ]]; then
    color="$RED"
  fi
  printf '%b[disk-guard][%s] %s%b\n' "$color" "$level" "$message" "$NC" >&2
  append_line "$DISK_ALERT_LOG" "$(date -u '+%Y-%m-%dT%H:%M:%SZ') [$level] $message"
}

# 采集一次快照到全局变量，report 与 check 共用。
LABEL="" TARGET="" DFS="" FS_SIZE="" FS_USED="" FS_AVAIL="" FS_PCT=0 FS_MOUNT=""
DTABLE="" IMAGE_TOTAL="" DANGLING=0 CACHE_SIZE="" CACHE_RECLAIM=""

snapshot() {
  LABEL="${1:-}"
  TARGET="$(guard_target)"
  read -r DFS FS_SIZE FS_USED FS_AVAIL FS_PCT FS_MOUNT < <(df_stats "$TARGET") || true
  FS_PCT="${FS_PCT%\%}"
  if ! [[ "${FS_PCT:-}" =~ ^[0-9]+$ ]]; then
    FS_PCT=0
  fi

  DTABLE="$(docker system df 2>/dev/null || true)"
  IMAGE_TOTAL="$(awk '$1=="Images" {print $2}' <<<"$DTABLE")"
  # "Build Cache" 是两词 TYPE，列整体右移一格: $3=TOTAL $4=ACTIVE $5=SIZE $6=RECLAIMABLE
  CACHE_SIZE="$(awk '$1=="Build" && $2=="Cache" {print $5}' <<<"$DTABLE")"
  CACHE_RECLAIM="$(awk '$1=="Build" && $2=="Cache" {print $6}' <<<"$DTABLE")"
  DANGLING="$(docker images -f dangling=true -q 2>/dev/null | wc -l | tr -d ' ' || true)"
  if ! [[ "${DANGLING:-}" =~ ^[0-9]+$ ]]; then
    DANGLING=0
  fi

  append_line "$DISK_USAGE_LOG" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"\
" label=${LABEL:-report} used_pct=$FS_PCT avail=${FS_AVAIL:-NA}"\
" images=${IMAGE_TOTAL:-NA} dangling=$DANGLING buildkit_cache=${CACHE_SIZE:-NA}"
}

do_report() {
  local title="磁盘快照"
  if [[ -n "$LABEL" ]]; then
    title="磁盘快照（$LABEL）"
  fi
  echo -e "${GREEN}[disk-guard] ── $title  $(date -u '+%Y-%m-%dT%H:%M:%SZ') ──${NC}"
  echo "[disk-guard] 文件系统: ${DFS:-unknown} 挂载于 ${FS_MOUNT:-?} — 已用 ${FS_PCT}%，可用 ${FS_AVAIL:-?}（共 ${FS_SIZE:-?}）"
  echo "[disk-guard] 检查路径: $TARGET"
  if [[ -n "${IMAGE_TOTAL:-}" ]]; then
    echo "[disk-guard] 镜像: $IMAGE_TOTAL 个（悬空 $DANGLING 个）"
    echo "[disk-guard] BuildKit 缓存: ${CACHE_SIZE:-?}（可回收 ${CACHE_RECLAIM:-?}）"
    echo "[disk-guard] Docker 资源明细:"
    sed 's/^/[disk-guard]   /' <<<"$DTABLE"
  else
    echo "[disk-guard] Docker 统计不可用（docker 命令失败或无响应）"
  fi
}

do_check() {
  if (( FS_PCT >= DISK_BLOCK_USED_PERCENT )); then
    emit_alert "BLOCK" "磁盘已用 ${FS_PCT}% ≥ 暂停线 ${DISK_BLOCK_USED_PERCENT}%（可用 ${FS_AVAIL:-?}）— 非必要构建应暂停；只预警，未自动清理任何数据"
    exit 2
  elif (( FS_PCT >= DISK_WARN_USED_PERCENT )); then
    emit_alert "WARN" "磁盘已用 ${FS_PCT}% ≥ 告警线 ${DISK_WARN_USED_PERCENT}%（可用 ${FS_AVAIL:-?}）— 请关注增长趋势；只预警，未自动清理任何数据"
    echo -e "${GREEN}[disk-guard][OK] 低于暂停线 ${DISK_BLOCK_USED_PERCENT}%，构建可继续${NC}"
    exit 0
  fi
  echo -e "${GREEN}[disk-guard][OK] 磁盘已用 ${FS_PCT}%，可用 ${FS_AVAIL:-?}（告警线 ${DISK_WARN_USED_PERCENT}% / 暂停线 ${DISK_BLOCK_USED_PERCENT}%）${NC}"
}

usage() {
  sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

case "${1:-help}" in
  report)
    snapshot "${2:-}"
    do_report
    ;;
  check)
    snapshot ""
    do_check
    ;;
  --help|-h|help)
    usage
    ;;
  *)
    echo "未知命令: $1（可用: report | check | help）" >&2
    exit 1
    ;;
esac
