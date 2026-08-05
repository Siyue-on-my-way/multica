#!/bin/bash

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}      Multica 服务重启脚本              ${NC}"
echo -e "${YELLOW}========================================${NC}"

# 进入 docker 目录（docker-compose.yml 和 .env 都在这里）
cd "$(dirname "$0")/docker"

echo -e "\n${GREEN}[1/4] 正在停止旧服务...${NC}"
docker-compose down

echo -e "\n${GREEN}[2/4] 正在拉取最新镜像 (可选)...${NC}"
docker-compose pull

echo -e "\n${GREEN}[3/4] 正在启动服务...${NC}"
docker-compose up -d

echo -e "\n${GREEN}[4/4] 检查服务状态...${NC}"
sleep 3
docker-compose ps

echo -e "\n${YELLOW}========================================${NC}"
echo -e "${GREEN}服务已成功拉起！${NC}"
echo -e "你可以通过以下命令查看实时日志："
echo -e "  ${YELLOW}docker-compose logs -f${NC}"
echo -e "或者查看特定服务的日志："
echo -e "  ${YELLOW}docker-compose logs -f multica-backend${NC}"
echo -e "${YELLOW}========================================${NC}"
