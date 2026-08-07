#!/bin/bash

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

NO_CACHE=""
if [[ "$1" == "--no-cache" ]]; then
  NO_CACHE="--no-cache"
  echo -e "${YELLOW}[模式] 强制全量重建（--no-cache）${NC}"
fi

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}      Multica 服务重启脚本              ${NC}"
echo -e "${YELLOW}========================================${NC}"

cd "$(dirname "$0")/docker"

echo -e "\n${GREEN}[1/6] 清理悬空镜像和 build 缓存（释放磁盘）...${NC}"
docker image prune -f
docker builder prune -f

echo -e "\n${GREEN}[2/6] 重新构建本地镜像（backend + frontend）...${NC}"
docker-compose build $NO_CACHE multica-backend multica-frontend
if [ $? -ne 0 ]; then
  echo -e "${RED}构建失败，终止启动。${NC}"
  exit 1
fi

echo -e "\n${GREEN}[3/6] 拉取第三方镜像（postgres / redis / nginx）...${NC}"
docker-compose pull multica-postgres multica-redis multica-nginx 2>/dev/null || true

echo -e "\n${GREEN}[4/6] 停止旧服务...${NC}"
docker-compose down

echo -e "\n${GREEN}[5/6] 启动所有服务...${NC}"
docker-compose up -d

echo -e "\n${GREEN}[6/6] 检查服务状态...${NC}"
sleep 5
docker-compose ps

echo -e "\n${YELLOW}========================================${NC}"
echo -e "${GREEN}服务已成功拉起！${NC}"
echo -e "查看实时日志："
echo -e "  ${YELLOW}docker-compose logs -f${NC}"
echo -e "查看特定服务日志："
echo -e "  ${YELLOW}docker-compose logs -f multica-backend${NC}"
echo -e "  ${YELLOW}docker-compose logs -f multica-frontend${NC}"
echo -e "${YELLOW}========================================${NC}"
