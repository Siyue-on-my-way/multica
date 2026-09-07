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
    --help|-h)
      echo "用法：$0 [--build] [--backend|--frontend] [--no-cache] [--restart-only]"
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

run_housekeeping() {
  if [[ -x "$DOCKER_HOUSEKEEPING_SCRIPT" ]]; then
    "$DOCKER_HOUSEKEEPING_SCRIPT"
  else
    echo -e "${YELLOW}未找到统一 Docker 清理脚本，跳过清理：$DOCKER_HOUSEKEEPING_SCRIPT${NC}" >&2
  fi
}

cd "$REPO_ROOT/docker"

echo -e "\n${GREEN}[1] 保留全局 Docker 缓存，由统一清理脚本集中维护${NC}"

# SIY-123 (2026-09-06): 任务完成 72h 后整环境回收（自托管默认 0 = 永不回收，磁盘无限增长）。
# daemon 的 GC 只读环境变量；此导出对 restart.sh 内的 daemon 重启生效，
# 手动启动 daemon 时由 /root/.bashrc 中的同名导出兜底。
export MULTICA_GC_COMPLETED_TASK_TTL="${MULTICA_GC_COMPLETED_TASK_TTL:-72h}"

if [[ "$SKIP_BUILD" == false ]]; then
  echo -e "\n${GREEN}[2] 使用共享构建锁重新构建镜像：${BUILD_TARGETS[*]}...${NC}"
  if ! run_locked_build; then
    echo -e "${RED}构建失败，终止启动。${NC}"
    exit 1
  fi
  BUILD_PERFORMED=true
else
  echo -e "\n${GREEN}[2] 跳过构建${NC}"
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
fi

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
echo -e "  ${YELLOW}./restart.sh --no-cache${NC}     # 显式全量重建，不使用已有缓存"
echo -e ""
echo -e "查看实时日志："
echo -e "  ${YELLOW}docker-compose -f docker/docker-compose.yml logs -f${NC}"
echo -e "${YELLOW}========================================${NC}"
