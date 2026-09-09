#!/usr/bin/env bash
# 停止 AIgo Docker Compose 部署；不删除数据库、证书、输出或备份数据卷。

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")" && pwd)"
DEPLOY_DIR="$PROJECT_ROOT/deploy"
ENV_FILE="$DEPLOY_DIR/production.env"

command -v docker >/dev/null 2>&1 || {
  echo "未找到 Docker 命令。" >&2
  exit 1
}

docker info >/dev/null 2>&1 || {
  echo "Docker 守护进程未运行，无法停止容器。" >&2
  exit 1
}

if [ ! -r "$ENV_FILE" ]; then
  echo "未找到 $ENV_FILE，无法确定要停止的 AIgo Compose 项目。" >&2
  exit 1
fi

echo "停止 AIgo Docker 服务（保留所有数据卷）..."
"$DEPLOY_DIR/compose.sh" down
echo "AIgo Docker 服务已停止；再次运行 ./docker-start.sh 可恢复启动。"
