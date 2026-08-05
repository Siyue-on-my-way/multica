#!/bin/bash

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}      Multica 服务停止脚本              ${NC}"
echo -e "${YELLOW}========================================${NC}"

# 进入项目根目录
cd "$(dirname "$0")"

echo -e "\n${GREEN}[1/2] 正在停止并移除容器...${NC}"
# 使用 -v 参数可以同时移除挂载的匿名卷(如果有的话)，但不会影响我们绑定的本地目录
docker-compose down

echo -e "\n${GREEN}[2/2] 检查剩余容器状态...${NC}"
docker-compose ps

echo -e "\n${YELLOW}========================================${NC}"
echo -e "${GREEN}服务已完全停止！${NC}"
echo -e "你的数据安全地保存在 ./docker/volumes/ 目录下。"
echo -e "${YELLOW}========================================${NC}"
