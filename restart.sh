#!/bin/bash

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

NO_CACHE=""
BUILD_TARGETS="multica-backend multica-frontend"
SKIP_BUILD=false

for arg in "$@"; do
  case "$arg" in
    --no-cache)
      NO_CACHE="--no-cache"
      echo -e "${YELLOW}[模式] 强制全量重建（--no-cache）${NC}"
      ;;
    --backend)
      BUILD_TARGETS="multica-backend"
      echo -e "${YELLOW}[模式] 仅重建后端${NC}"
      ;;
    --frontend)
      BUILD_TARGETS="multica-frontend"
      echo -e "${YELLOW}[模式] 仅重建前端${NC}"
      ;;
    --restart-only)
      SKIP_BUILD=true
      echo -e "${YELLOW}[模式] 跳过构建，仅重启容器${NC}"
      ;;
  esac
done

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}      Multica 服务重启脚本              ${NC}"
echo -e "${YELLOW}========================================${NC}"

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO_ROOT/docker"

if [[ -n "$NO_CACHE" ]]; then
  echo -e "\n${GREEN}[1] 清理悬空镜像和 build 缓存（释放磁盘）...${NC}"
  docker image prune -f
  docker builder prune -f
else
  echo -e "\n${GREEN}[1] 跳过缓存清理（保留分层缓存以加速构建）${NC}"
fi

if [[ "$SKIP_BUILD" == false ]]; then
  echo -e "\n${GREEN}[2] 重新构建镜像：${BUILD_TARGETS}...${NC}"
  docker-compose build $NO_CACHE $BUILD_TARGETS
  if [ $? -ne 0 ]; then
    echo -e "${RED}构建失败，终止启动。${NC}"
    exit 1
  fi
else
  echo -e "\n${GREEN}[2] 跳过构建${NC}"
fi

# 当 backend 重建时，同步更新本机 daemon 二进制，确保 /api/daemon/binary 下发的版本与本机一致
if [[ "$SKIP_BUILD" == false ]] && [[ "$BUILD_TARGETS" == *"multica-backend"* ]]; then
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

echo -e "\n${GREEN}[6] 检查服务状态...${NC}"
sleep 5
docker-compose ps

echo -e "\n${YELLOW}========================================${NC}"
echo -e "${GREEN}服务已成功拉起！${NC}"
echo -e ""
echo -e "用法："
echo -e "  ${YELLOW}./restart.sh${NC}                # 增量构建前后端（利用缓存）"
echo -e "  ${YELLOW}./restart.sh --backend${NC}      # 仅重建后端"
echo -e "  ${YELLOW}./restart.sh --frontend${NC}     # 仅重建前端"
echo -e "  ${YELLOW}./restart.sh --restart-only${NC} # 跳过构建，直接重启"
echo -e "  ${YELLOW}./restart.sh --no-cache${NC}     # 全量重建（清除所有缓存）"
echo -e ""
echo -e "查看实时日志："
echo -e "  ${YELLOW}docker-compose -f docker/docker-compose.yml logs -f${NC}"
echo -e "${YELLOW}========================================${NC}"
