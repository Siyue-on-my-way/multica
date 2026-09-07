#!/bin/bash

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

NO_CACHE_ARGS=()
BUILD_TARGETS=(multica-backend multica-frontend)
# Restarting a service should reuse its current image. Building is explicit so
# a routine restart cannot create a new image just because the script ran.
SKIP_BUILD=true
BUILD_PERFORMED=false
FORCE_BUILD=false

for arg in "$@"; do
  case "$arg" in
    --build)
      SKIP_BUILD=false
      echo -e "${YELLOW}[模式] 使用缓存构建后端和前端${NC}"
      ;;
    --no-cache)
      NO_CACHE_ARGS+=(--no-cache)
      SKIP_BUILD=false
      echo -e "${YELLOW}[模式] 强制全量重建（--no-cache）${NC}"
      ;;
    --backend)
      BUILD_TARGETS=(multica-backend)
      SKIP_BUILD=false
      echo -e "${YELLOW}[模式] 仅重建后端${NC}"
      ;;
    --frontend)
      BUILD_TARGETS=(multica-frontend)
      SKIP_BUILD=false
      echo -e "${YELLOW}[模式] 仅重建前端${NC}"
      ;;
    --restart-only)
      SKIP_BUILD=true
      echo -e "${YELLOW}[模式] 跳过构建，仅重启容器${NC}"
      ;;
    --force-build)
      FORCE_BUILD=true
      echo -e "${YELLOW}[模式] 磁盘空间不足时仍强制构建（越过预警暂停线，需自行确认空间）${NC}"
      ;;
    --help|-h)
      echo "用法：$0 [--build] [--backend|--frontend] [--no-cache] [--force-build] [--restart-only]"
      exit 0
      ;;
  esac
done

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}      Multica 服务重启脚本              ${NC}"
echo -e "${YELLOW}========================================${NC}"

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
DOCKER_BUILD_LOCK_FILE="${DOCKER_BUILD_LOCK_FILE:-/tmp/multica-docker-build.lock}"
DOCKER_HOUSEKEEPING_SCRIPT="${DOCKER_HOUSEKEEPING_SCRIPT:-$REPO_ROOT/scripts/docker-housekeeping.sh}"
DISK_GUARD_SCRIPT="${DISK_GUARD_SCRIPT:-$REPO_ROOT/scripts/disk-guard.sh}"
# SIY-153: 用户上传数据生命周期巡检。默认 dry-run 只报告，重启路径永不删除；
# 设 UPLOADS_GC_REPORT_ON_RESTART=0 可关闭。
UPLOADS_GC_SCRIPT="${UPLOADS_GC_SCRIPT:-$REPO_ROOT/scripts/uploads-gc.sh}"

run_locked_build() {
  command -v flock >/dev/null 2>&1 || {
    echo -e "${RED}未找到 flock，拒绝在没有构建锁的情况下执行 Docker 构建。${NC}" >&2
    return 1
  }
  mkdir -p -- "$(dirname -- "$DOCKER_BUILD_LOCK_FILE")"
  (
    exec 9>"$DOCKER_BUILD_LOCK_FILE"
    flock 9
    docker-compose build "${NO_CACHE_ARGS[@]}" "${BUILD_TARGETS[@]}"
  )
}

run_disk_guard() {
  if [[ -x "$DISK_GUARD_SCRIPT" ]]; then
    "$DISK_GUARD_SCRIPT" "$@"
  else
    echo -e "${YELLOW}未找到磁盘监控脚本，跳过磁盘快照/预警：$DISK_GUARD_SCRIPT${NC}" >&2
  fi
}

run_housekeeping() {
  if [[ -x "$DOCKER_HOUSEKEEPING_SCRIPT" ]]; then
    "$DOCKER_HOUSEKEEPING_SCRIPT"
  else
    echo -e "${YELLOW}未找到统一 Docker 清理脚本，跳过清理：$DOCKER_HOUSEKEEPING_SCRIPT${NC}" >&2
  fi
}

# 上传数据生命周期巡检：脚本默认 dry-run，只报告引用/孤儿/宽限期状态。
# 数据库不可达时脚本自身会失败，这里只打警告，绝不影响重启。
run_uploads_gc_report() {
  if [[ "${UPLOADS_GC_REPORT_ON_RESTART:-1}" != "1" ]]; then
    return 0
  fi
  if [[ -x "$UPLOADS_GC_SCRIPT" ]]; then
    "$UPLOADS_GC_SCRIPT" || echo -e "${YELLOW}[uploads-gc] 上传数据巡检未完成（不影响重启；重启路径永远不会自动删除上传数据）${NC}" >&2
  else
    echo -e "${YELLOW}未找到上传数据巡检脚本，跳过：$UPLOADS_GC_SCRIPT${NC}" >&2
  fi
}

cd "$REPO_ROOT/docker"

echo -e "\n${GREEN}[1] 保留全局 Docker 缓存，由统一清理脚本集中维护${NC}"

# SIY-123 (2026-09-06): 任务完成 72h 后整环境回收（自托管默认 0 = 永不回收，磁盘无限增长）。
# daemon 的 GC 只读环境变量；此导出对 restart.sh 内的 daemon 重启生效，
# 手动启动 daemon 时由 /root/.bashrc 中的同名导出兜底。
export MULTICA_GC_COMPLETED_TASK_TTL="${MULTICA_GC_COMPLETED_TASK_TTL:-72h}"

if [[ "$SKIP_BUILD" == false ]]; then
  echo -e "\n${GREEN}[1.5] 构建前磁盘快照与预警检查（SIY-149）...${NC}"
  run_disk_guard report "构建前"
  run_disk_guard check
  check_rc=$?
  if (( check_rc == 2 )); then
    if [[ "$FORCE_BUILD" == true ]]; then
      echo -e "${YELLOW}[磁盘预警] --force-build 已指定：越过暂停线继续构建（未自动清理任何数据）${NC}"
    else
      echo -e "${RED}[磁盘预警] 可用空间已低于暂停线，本次非必要构建已暂停（未删除任何数据）。${NC}"
      echo -e "${RED}服务将使用现有镜像继续重启；清理磁盘后重试构建，或确认空间后用 --force-build 强制构建。${NC}"
      SKIP_BUILD=true
    fi
  elif (( check_rc != 0 )); then
    echo -e "${YELLOW}[磁盘预警] 磁盘检查异常退出（rc=$check_rc），不阻塞本次构建${NC}"
  fi
fi

if [[ "$SKIP_BUILD" == false ]]; then
  echo -e "\n${GREEN}[2] 使用共享构建锁重新构建镜像：${BUILD_TARGETS[*]}...${NC}"
  if ! run_locked_build; then
    echo -e "${RED}构建失败，终止启动。${NC}"
    exit 1
  fi
  BUILD_PERFORMED=true
  echo -e "\n${GREEN}[2.1] 构建后磁盘快照（对比 [1.5] 可见本次构建的磁盘与缓存消耗）...${NC}"
  run_disk_guard report "构建后"
else
  echo -e "\n${GREEN}[2] 跳过构建${NC}"
  # 日常重启不构建：预警照常记录，但不阻塞重启（up -d 复用现有镜像）。
  run_disk_guard check || true
fi

# 当 backend 重建时，同步更新本机 daemon 二进制，确保 /api/daemon/binary 下发的版本与本机一致
if [[ "$SKIP_BUILD" == false ]] && [[ " ${BUILD_TARGETS[*]} " == *" multica-backend "* ]]; then
  echo -e "\n${GREEN}[2.5] 重建本机 daemon 二进制并更新 /usr/local/bin/multica...${NC}"
  VERSION=$(git -C "$REPO_ROOT" describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
  COMMIT=$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)
  DATE=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  (cd "$REPO_ROOT/server" && CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" -o bin/multica ./cmd/multica)
  if [ $? -eq 0 ]; then
    sudo rm -f /usr/local/bin/multica
    sudo cp "$REPO_ROOT/server/bin/multica" /usr/local/bin/multica
    echo -e "${GREEN}  daemon 二进制已更新${NC}"
    if multica daemon status 2>/dev/null | grep -q "running"; then
      echo -e "${GREEN}  重启 daemon...${NC}"
      multica daemon restart --profile local
    else
      echo -e "${YELLOW}  daemon 未运行，跳过重启（如需启动请执行: multica daemon start --profile local）${NC}"
    fi
  else
    echo -e "${YELLOW}  警告：daemon 二进制构建失败，/usr/local/bin/multica 未更新${NC}"
  fi
fi

echo -e "\n${GREEN}[3] 拉取第三方镜像（postgres / redis / nginx）...${NC}"
docker-compose pull multica-postgres multica-redis multica-nginx 2>/dev/null || true

echo -e "\n${GREEN}[4] 停止旧服务...${NC}"
docker-compose down

echo -e "\n${GREEN}[5] 启动所有服务...${NC}"
docker-compose up -d

if [[ "$BUILD_PERFORMED" == true ]]; then
  echo -e "\n${GREEN}[5.1] 执行统一 Docker 垃圾回收策略...${NC}"
  run_housekeeping
  echo -e "\n${GREEN}[5.2] 清理后磁盘快照（对比 [2.1] 可见本次回收效果）...${NC}"
  run_disk_guard report "清理后"
fi

echo -e "\n${GREEN}[5.3] 用户上传数据生命周期巡检（SIY-153，默认只报告不删除）...${NC}"
run_uploads_gc_report

echo -e "\n${GREEN}[6] 检查服务状态...${NC}"
sleep 5
docker-compose ps

echo -e "\n${YELLOW}========================================${NC}"
echo -e "${GREEN}服务已成功拉起！${NC}"
echo -e ""
echo -e "用法："
echo -e "  ${YELLOW}./restart.sh${NC}                # 使用现有镜像重启服务"
echo -e "  ${YELLOW}./restart.sh --build${NC}        # 增量构建后端和前端，再重启"
echo -e "  ${YELLOW}./restart.sh --backend${NC}      # 使用缓存仅重建后端"
echo -e "  ${YELLOW}./restart.sh --frontend${NC}     # 使用缓存仅重建前端"
echo -e "  ${YELLOW}./restart.sh --restart-only${NC} # 跳过构建，直接重启（兼容别名）"
echo -e "  ${YELLOW}./restart.sh --force-build${NC}  # 磁盘低于暂停线时仍强制构建（默认会暂停构建）"
echo -e "  ${YELLOW}./restart.sh --no-cache${NC}     # 显式全量重建，不使用已有缓存"
echo -e ""
echo -e "查看实时日志："
echo -e "  ${YELLOW}docker-compose -f docker/docker-compose.yml logs -f${NC}"
echo -e "${YELLOW}========================================${NC}"
